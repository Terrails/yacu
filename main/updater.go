package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/types/config"
	"github.com/terrails/yacu/types/database"
	"github.com/terrails/yacu/types/docker"
	yacuimage "github.com/terrails/yacu/types/image"
	"github.com/terrails/yacu/types/set"
	"github.com/terrails/yacu/types/webhook"
	"github.com/terrails/yacu/utils"
	"golang.org/x/exp/maps"

	yacutypes "github.com/terrails/yacu/types"
	yacucontainer "github.com/terrails/yacu/types/container"
	yacuregistry "github.com/terrails/yacu/types/registry"
)

// suffix given to a container while its replacement is being created
const oldContainerSuffix = "-yacu-old"

// how often checking a single container is attempted before giving up,
// and the base delay between attempts
const scanAttempts = 3

var scanRetryDelay = time.Second * 2

// how long pulling a single image may take
const pullTimeout = 30 * time.Minute

type Yacu struct {
	Client   docker.API
	Webhooks *webhook.Webhooks

	DB         database.Database
	Scanner    config.Scanner
	Updater    config.Updater
	Registries config.RegistryEntries
}

// A failed step of an image or container update. Context names the step that failed.
type updateError struct {
	Context string
	Err     error
}

func (e *updateError) Error() string { return fmt.Sprintf("%s: %v", e.Context, e.Err) }
func (e *updateError) Unwrap() error { return e.Err }

func (app Yacu) Run(ctx context.Context) {
	logger := zerolog.Ctx(ctx)

	containers, errs := app.FetchUpdates(ctx)
	if ctx.Err() != nil {
		// errors caused by the interruption are not worth reporting
		logger.Info().Msg("Shutting down, scan interrupted")
		return
	}

	if len(errs) > 0 {
		logger.Warn().Errs("errors", errs).Msg("Failed to fetch some updates")
		for _, err := range errs {
			app.Webhooks.Error(ctx, "Unable to fetch updates", err)
		}

		if app.Scanner.FailOnError {
			logger.Error().Msg("Not applying updates because scanning failed (scanner.fail_on_error)")
			return
		}
	}

	if containers == nil {
		return
	}

	if len(containers) == 0 {
		logger.Info().Msg("No new updates found")
	} else {
		logger.Info().Int("count", len(containers)).Msg("Found new updates")
	}

	app.ApplyUpdates(ctx, containers)
}

// Pulls the new images and recreates the given containers.
// A container is skipped when its image could not be pulled.
// On shutdown (ctx cancelled) an update in progress is completed, the remaining ones are skipped.
func (app Yacu) ApplyUpdates(ctx context.Context, containers yacucontainer.Containers) {
	logger := zerolog.Ctx(ctx)

	failedImages := app.PullImages(ctx, containers)

	// containers joining another's namespaces go first. Updating that other container
	// recreates them, after which the containers scanned here no longer exist
	containers = slices.Clone(containers)
	slices.SortStableFunc(containers, func(a, b *yacucontainer.Container) int {
		switch aShares, bShares := sharesNamespace(a), sharesNamespace(b); {
		case aShares && !bShares:
			return -1
		case !aShares && bShares:
			return 1
		}
		return 0
	})

	successCount := 0
	imgToRemove := set.NewImageSet()

	for i, cnt := range containers {
		if ctx.Err() != nil {
			logger.Warn().Int("skipped", len(containers)-i).Msg("Shutting down, skipping remaining container updates")
			break
		}

		containerLogger := logger.With().Str("service", "container_update").Str("container", cnt.Name).Str("image", cnt.RepositoryFamiliarized()).Logger()
		containerCtx := containerLogger.WithContext(ctx)

		if _, failed := failedImages[cnt.Repository.String()]; failed {
			containerLogger.Warn().Msg("Skipping container update as its image could not be pulled")
			continue
		}

		containerLogger.Debug().Msg("Updating container")

		newContainer, warnings, err := app.UpdateContainer(containerCtx, cnt)
		if err != nil {
			containerLogger.Err(err).Msg("Failed to update container")
			var updateErr *updateError
			if errors.As(err, &updateErr) {
				app.Webhooks.ContainerError(containerCtx, cnt, updateErr.Context, updateErr.Err)
			} else {
				app.Webhooks.ContainerError(containerCtx, cnt, "Unable to update container", err)
			}
			continue
		}

		successCount += 1
		imgToRemove.Add(cnt.Image)
		containerLogger.Info().Msg("Updated container")
		app.Webhooks.ContainerUpdated(containerCtx, cnt, newContainer, warnings...)
	}

	logger.Info().Int("total", len(containers)).Int("successful", successCount).Msg("Container updates completed")

	if app.Updater.RemoveImages && len(imgToRemove.Items) > 0 && ctx.Err() == nil {
		logger.Debug().Int("count", len(imgToRemove.Items)).Msg("Removing unused images")
		count := app.RemoveUnusedImages(ctx, maps.Values(imgToRemove.Items)...)
		logger.Info().Int("count", count).Msg("Removed unused images")
	}
}

// Pulls the new image of every container.
// The returned map holds the repositories that failed.
func (app Yacu) PullImages(ctx context.Context, containers yacucontainer.Containers) map[string]error {
	logger := zerolog.Ctx(ctx)

	failed := map[string]error{}
	handled := map[string]bool{}

	for _, cnt := range containers {
		repository := cnt.Repository.String()
		if handled[repository] {
			continue
		}
		handled[repository] = true

		if ctx.Err() != nil {
			failed[repository] = ctx.Err()
			continue
		}

		imageLogger := logger.With().Str("service", "image_pull").Str("image", cnt.RepositoryFamiliarized()).Logger()
		imageCtx := imageLogger.WithContext(ctx)

		if err := app.pullNewImage(imageCtx, cnt); err != nil {
			failed[repository] = err.Err
			// a pull cut short by shutdown is not worth reporting
			if ctx.Err() == nil {
				app.Webhooks.ImageError(imageCtx, cnt.Image, err.Context, err.Err)
			}
		}
	}
	return failed
}

func (app Yacu) pullNewImage(ctx context.Context, cnt *yacucontainer.Container) *updateError {
	logger := zerolog.Ctx(ctx)

	// the image may already be present, e.g. pulled manually since the scan
	if yes, err := app.IsLatestImagePresent(ctx, cnt.Repository); err != nil {
		return &updateError{Context: "Unable to check if image is latest", Err: err}
	} else if yes {
		return nil
	}

	logger.Debug().Msg("Pulling image")

	if err := app.PullImage(ctx, cnt.Repository); err != nil {
		return &updateError{Context: "Unable to pull image", Err: err}
	}

	newImageRaw, err := app.Client.ImageInspect(ctx, cnt.Repository.String())
	if err != nil {
		logger.Err(err).Msg("ImageInspect request failed")
		return &updateError{Context: "Unable to inspect image", Err: err}
	}

	newImageData, err := yacuimage.NewData(&newImageRaw, cnt.Repository)
	if err != nil {
		return &updateError{Context: "Unable to initialize image", Err: err}
	}

	logger.Info().Msg("Pulled image")
	app.Webhooks.ImageUpdated(ctx, cnt.Image, newImageData)
	return nil
}

// Recreates cnt from its (already pulled) image.
//
// The old container is renamed rather than removed until its replacement has
// been created and started, so that any failure can be rolled back: the
// replacement is removed, the old container gets its name back and it (and
// any stopped dependants) is started again.
//
// Containers joining a namespace of cnt (network_mode container:<cnt>) are
// stopped with it and made to join the replacement.
//
// Once started, an update is not interrupted by shutdown (ctx cancelled) so
// that it always ends committed or rolled back. Only waiting on the conditions
// of dependants is cut short, those still waiting are left stopped.
func (app Yacu) UpdateContainer(ctx context.Context, cnt *yacucontainer.Container) (*yacucontainer.Container, []string, error) {
	logger := zerolog.Ctx(ctx)

	shutdownCtx := ctx
	ctx = context.WithoutCancel(ctx)

	warnings := []string{}
	shouldRestart := cnt.IsRunning()
	var dependantContainers yacucontainer.DependantContainers
	var children namespaceChildren

	// fail undoes the steps taken so far (most recent first), restarts the old
	// container and its dependants if they were running, and returns the error
	fail := func(context string, err error, undo ...func() error) error {
		var rollbackErrs []error
		for _, fn := range undo {
			if undoErr := fn(); undoErr != nil {
				rollbackErrs = append(rollbackErrs, undoErr)
			}
		}

		if shouldRestart {
			if startErr := cnt.Start(ctx, app.Client); startErr != nil {
				rollbackErrs = append(rollbackErrs, startErr)
			} else {
				rollbackErrs = append(rollbackErrs, app.startNamespaceChildren(ctx, children)...)
				for _, warning := range dependantContainers.Start(shutdownCtx, app.Client, cnt.ID) {
					rollbackErrs = append(rollbackErrs, errors.New(warning))
				}
			}
		}

		if len(rollbackErrs) > 0 {
			logger.Error().Errs("errors", rollbackErrs).Msg("Rolling back container update failed")
			err = errors.Join(err, fmt.Errorf("rollback failed: %w", errors.Join(rollbackErrs...)))
		} else {
			logger.Info().Msg("Rolled back container update")
		}
		return &updateError{Context: context, Err: err}
	}

	// what the replacement is created from, already pulled under the container's tag
	newImage, err := app.Client.ImageInspect(ctx, cnt.Repository.String())
	if err != nil {
		logger.Err(err).Msg("ImageInspect request failed")
		return nil, nil, &updateError{Context: "Unable to inspect image", Err: err}
	}

	if shouldRestart {
		dependantContainers, err = app.GetDependingContainers(ctx, cnt.Raw)
		if err != nil {
			return nil, nil, &updateError{Context: "Unable to fetch depending containers", Err: err}
		}
	}

	// needed even if cnt is not running, as children referencing it by ID could never start again otherwise
	children, err = app.GetNamespaceChildren(ctx, cnt.Raw, dependantContainers)
	if err != nil {
		return nil, nil, &updateError{Context: "Unable to fetch containers sharing namespaces", Err: err}
	}
	// compose makes a child depend on its parent, but it is handled as a child
	dependantContainers = slices.DeleteFunc(dependantContainers, func(d *yacucontainer.DependantContainer) bool {
		return children.contains(d.Data.ID)
	})

	if shouldRestart {
		stopWarnings := dependantContainers.Stop(ctx, app.Client)
		stopWarnings = append(stopWarnings, app.stopNamespaceChildren(ctx, children)...)
		if len(stopWarnings) > 0 {
			warnings = append(warnings, stopWarnings...)
			logger.Warn().Strs("warnings", stopWarnings).Msg("Received warnings while stopping depending containers")
		}

		if err = cnt.Stop(ctx, app.Client); err != nil {
			return nil, nil, fail("Unable to stop container", err)
		}
	}

	name := strings.TrimPrefix(cnt.Name, "/")
	oldName := name + oldContainerSuffix

	logger.Debug().Str("name", oldName).Msg("Renaming container")
	if err := app.Client.ContainerRename(ctx, cnt.ID, oldName); err != nil {
		logger.Err(err).Msg("Failed to rename container")
		return nil, nil, fail("Unable to rename container", err)
	}
	renameBack := func() error {
		if err := app.Client.ContainerRename(ctx, cnt.ID, name); err != nil {
			return fmt.Errorf("renaming %s back to %s failed: %w", oldName, name, err)
		}
		return nil
	}

	endpoints := cnt.EndpointsConfig()

	var singleNetSettings network.NetworkingConfig = network.NetworkingConfig{}
	for netName, netSettings := range endpoints {
		singleNetSettings.EndpointsConfig = map[string]*network.EndpointSettings{
			netName: netSettings,
		}
		break
	}

	logger.Debug().Msg("Creating container")
	response, err := app.Client.ContainerCreate(ctx, cnt.CreateConfig(), cnt.CreateHostConfig(newImage.Config), &singleNetSettings, nil, name)
	if err != nil {
		logger.Err(err).Msg("Failed to create container")
		return nil, nil, fail("Unable to create container", err, renameBack)
	}

	newId := response.ID
	removeNew := func() error {
		// the replacement's anonymous volumes are fresh and unused
		if err := app.Client.ContainerRemove(ctx, newId, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			return fmt.Errorf("removing new container %s failed: %w", utils.ShortId(newId), err)
		}
		return nil
	}

	if len(response.Warnings) > 0 {
		warnings = append(warnings, response.Warnings...)
		logger.Warn().Strs("warnings", response.Warnings).Msg("Received warnings while creating container")
	}

	// cannot use multiple networks if host networking is enabled
	if !cnt.Raw.HostConfig.NetworkMode.IsHost() {

		// should be already connected to 1 network
		if len(endpoints) > 1 {
			logger.Debug().Msg("Connecting container to networks")
		}

		// Add other networks
		for netName, netSettings := range endpoints {

			// skip already connected
			if _, ok := singleNetSettings.EndpointsConfig[netName]; ok {
				continue
			}

			logger.Debug().Str("network", netName).Msg("Connecting container to network")

			if err := app.Client.NetworkConnect(ctx, netName, newId, netSettings); err != nil {
				logger.Err(err).Str("network", netName).Msg("Connecting to network failed")
				// since we already came this far, might as well do everything and send a warning about it
				warnings = append(warnings, fmt.Sprintf("connecting to network %s failed: %v", netName, err))
			}
		}
	}

	newData, err := app.Client.ContainerInspect(ctx, newId)
	if err != nil {
		logger.Err(err).Str("id", newId).Msg("ContainerInspect request failed")
		return nil, nil, fail("Unable to inspect container", err, removeNew, renameBack)
	}

	newContainer, err := yacucontainer.New(ctx, app.Client, &newData, app.Updater.StopTimeout, app.Scanner.ImageAge)
	if err != nil {
		logger.Err(err).Str("container", newData.Name).Msg("Initializing recreated container failed")
		return nil, nil, fail("Unable to initialize container", err, removeNew, renameBack)
	}

	if shouldRestart {
		if err = newContainer.Start(ctx, app.Client); err != nil {
			return nil, nil, fail("Unable to start container", err, removeNew, renameBack)
		}
	}

	// before the previous container is removed, which children may still reference
	if len(children) > 0 {
		logger.Debug().Int("count", len(children)).Msg("Moving containers sharing namespaces to the new container")
		childWarnings := app.reattachNamespaceChildren(ctx, children, cnt.Raw, newId)
		if len(childWarnings) > 0 {
			warnings = append(warnings, childWarnings...)
			logger.Warn().Strs("warnings", childWarnings).Msg("Received warnings while moving containers sharing namespaces")
		}
	}

	// the replacement is in place, the old container is no longer needed
	logger.Debug().Str("name", oldName).Msg("Removing previous container")
	if err := app.Client.ContainerRemove(ctx, cnt.ID,
		container.RemoveOptions{
			Force:         true,
			RemoveVolumes: app.Updater.RemoveVolumes,
		},
	); err != nil {
		logger.Err(err).Str("name", oldName).Msg("Failed to remove previous container")
		warnings = append(warnings, fmt.Sprintf("removing previous container %s failed: %v", oldName, err))
	}

	if shouldRestart {
		startWarnings := dependantContainers.Start(shutdownCtx, app.Client, newId)
		if len(startWarnings) > 0 {
			warnings = append(warnings, startWarnings...)
			logger.Warn().Strs("warnings", startWarnings).Msg("Received warnings while starting depending containers")
		}
	}

	return newContainer, warnings, nil
}

func (app Yacu) FetchUpdates(ctx context.Context) (yacucontainer.Containers, []error) {
	logger := zerolog.Ctx(ctx).With().Str("service", "scanner").Logger()
	ctx = logger.WithContext(ctx)

	cntList, err := app.Client.ContainerList(
		ctx,
		container.ListOptions{All: true},
	)

	if err != nil {
		return nil, []error{fmt.Errorf("listing containers failed: %w", err)}
	}

	containers := yacucontainer.Containers{}
	var updateErrors []error = []error{}

	for i := range cntList {
		if ctx.Err() != nil {
			break
		}
		summary := &cntList[i]

		if !yacucontainer.ShouldScan(summary, app.Scanner.ScanAll, app.Scanner.ScanStopped) {
			continue
		}

		if container, err := app.checkContainerWithRetry(ctx, summary); err != nil {
			updateErrors = append(updateErrors, err)
		} else if container != nil {
			containers = append(containers, container)
		}
	}

	return containers, updateErrors
}

func (app Yacu) checkContainerWithRetry(ctx context.Context, summary *container.Summary) (*yacucontainer.Container, error) {
	logger := zerolog.Ctx(ctx)

	var err error
	for attempt := 1; attempt <= scanAttempts; attempt++ {
		var cnt *yacucontainer.Container
		if cnt, err = app.CheckIfContainerIsUpdateable(ctx, summary); err == nil {
			return cnt, nil
		}

		logger.Debug().Err(err).Str("container", summary.ID).Int("attempt", attempt).Msg("Failed to check if container is updateable")
		if attempt < scanAttempts {
			if utils.Sleep(ctx, scanRetryDelay*time.Duration(attempt)) != nil {
				break
			}
		}
	}

	var containerName string
	if len(summary.Names) > 0 {
		containerName = strings.TrimPrefix(summary.Names[0], "/")
	} else {
		containerName = summary.ID
	}

	return nil, fmt.Errorf("checking if container %s is updateable failed: %w", containerName, err)
}

func (app Yacu) CheckIfContainerIsUpdateable(ctx context.Context, c *container.Summary) (*yacucontainer.Container, error) {
	logger := zerolog.Ctx(ctx).With().
		Str("container", c.ID).
		Str("image", c.Image).
		Logger()
	ctx = logger.WithContext(ctx)

	// fetch detailed info
	ci, err := app.Client.ContainerInspect(ctx, c.ID)
	if err != nil {
		logger.Err(err).Str("id", c.ID).Msg("ContainerInspect request failed")
		return nil, fmt.Errorf("inspecting container %s failed: %w", c.ID, err)
	}

	container, err := yacucontainer.New(ctx, app.Client, &ci, app.Updater.StopTimeout, app.Scanner.ImageAge)
	if err != nil {
		if yacutypes.IsUnsupportedImage(err) {
			// local builds, digest references and pinned tags have no updates to pull
			logger.Debug().Err(err).Str("container", ci.Name).Msg("Skipping container with unsupported image")
			return nil, nil
		}
		logger.Err(err).Str("container", ci.Name).Msg("Container initialization failed")
		return nil, fmt.Errorf("initializing container %s failed: %w", ci.Name, err)
	}

	// do not update self
	if container.IsYacu() {
		return nil, nil
	}

	if yes, err := container.IsOutdated(); err != nil {
		return nil, fmt.Errorf("checking if container %s is outdated failed: %w", ci.Name, err)
	} else if yes {
		if yes, err = app.IsRemotePullable(ctx, container); err != nil {
			return nil, fmt.Errorf("checking if image for container %s is pullable failed: %w", ci.Name, err)
		} else if yes {
			return container, nil
		}
	}
	return nil, nil
}

func (app Yacu) IsRemotePullable(ctx context.Context, container *yacucontainer.Container) (bool, error) {
	logger := zerolog.Ctx(ctx).With().
		Str("container", container.Name).
		Str("image", container.RepositoryFamiliarized()).
		Logger()
	ctx = logger.WithContext(ctx)

	familiarNameTagged := container.RepositoryFamiliarized()

	dbImage, err := app.DB.GetRemoteImageFromName(familiarNameTagged)
	if err != nil {
		// image data not present
		if errors.Is(err, sql.ErrNoRows) {
			// fetch data from registry
			remoteData, err := yacuregistry.GetImageDataFromRegistry(ctx, &app.Registries, container.Repository)
			if err != nil {
				return false, err
			}

			// write data to local database
			if _, err := app.DB.SaveRemoteImage(
				familiarNameTagged,
				reference.Domain(container.Repository),
				*remoteData.Created,
				remoteData.Digest,
			); err != nil {
				logger.Err(err).Msg("Writing remote image data to local database failed")
				return false, fmt.Errorf("writing remote image data (%s) to local database failed: %w", familiarNameTagged, err)
			}

			// recheck if container is old enough
			if utils.DaysPassed(*remoteData.Created) < container.MinImageAge {
				logger.Debug().Msg("Image up to date")
				return false, nil
			}

			// check if remote and local images are different
			if container.HasRepoDigest(remoteData.Digest) {
				return false, nil
			}

			// outdated
			logger.Debug().Msg("Image added to update queue")
			return true, nil
		} else {
			// unknown SQL error
			logger.Err(err).Msg("Fetching remote image data from local database failed")
			return false, fmt.Errorf("fetching remote image data (%s) from local database failed: %w", familiarNameTagged, err)
		}
	}

	// check if image data in database is old enough
	if utils.DaysPassed(dbImage.Created) < container.MinImageAge {
		logger.Debug().Msg("Image up to date")
		return false, nil
	}

	// last check should have been done at least an interval enough ago
	if utils.DaysPassed(dbImage.LastCheck) < container.MinImageAge {
		logger.Debug().Msg("Image up to date")
		return false, nil
	}

	// fetch new data from registry
	remoteData, err := yacuregistry.GetImageDataFromRegistry(ctx, &app.Registries, container.Repository)
	if err != nil {
		return false, err
	}

	// write new data to db
	if err := app.DB.UpdateRemoteImage(dbImage.RowId, remoteData.Created, &remoteData.Digest); err != nil {
		logger.Err(err).Msg("Writing remote image data to local database failed")
		return false, fmt.Errorf("writing remote image data (%s) to local database failed: %w", familiarNameTagged, err)
	}

	// recheck if image is old enough to pull
	if utils.DaysPassed(*remoteData.Created) < container.MinImageAge {
		logger.Debug().Msg("Image up to date")
		return false, nil
	}

	// update last check time
	if err := app.DB.UpdateRemoteImageCheck(dbImage.RowId); err != nil {
		logger.Err(err).Msg("Updating remote image data in local database failed")
		return false, fmt.Errorf("updating remote image data (%s) in local database failed: %w", familiarNameTagged, err)
	}

	// check if remote and local images are different
	if container.HasRepoDigest(remoteData.Digest) {
		return false, nil
	}

	// outdated
	logger.Debug().Msg("Image added to update queue")
	return true, nil
}

func (app Yacu) IsLatestImagePresent(ctx context.Context, named reference.NamedTagged) (bool, error) {
	logger := zerolog.Ctx(ctx)

	currentImgData, err := app.Client.ImageInspect(ctx, named.String())
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			// the tag can be gone while the container still runs the image it was created
			// from, e.g. after `docker image rm -f` or pruning the image the tag moved to
			logger.Debug().Msg("Image not present locally")
			return false, nil
		}
		logger.Err(err).Msg("InspectImage request failed")
		return false, fmt.Errorf("inspecting image %s failed: %w", named.String(), err)
	}

	familiarNameTagged := utils.FamiliarTagged(named)

	dbImage, err := app.DB.GetRemoteImageFromName(familiarNameTagged)
	if err != nil {
		logger.Err(err).Msg("Fetching remote image data from local database failed")
		return false, fmt.Errorf("fetching remote image data (%s) from local database failed: %w", named.String(), err)
	}

	if createdTime, err := time.Parse(time.RFC3339Nano, currentImgData.Created); err != nil {
		logger.Err(err).Str("time", currentImgData.Created).Msg("unknown time format")
		return false, fmt.Errorf("unknown time format %s: %w", currentImgData.Created, err)
	} else if createdTime.Compare(dbImage.Created) == 0 {
		return true, nil
	}

	for _, str := range currentImgData.RepoDigests {
		if strings.Contains(str, dbImage.Digest.String()) {
			return true, nil
		}
	}
	return false, nil
}

// pullMessage is the part of the image pull progress stream that reports errors.
// The daemon reports pull failures inside the stream, not as an API error.
type pullMessage struct {
	Error       string `json:"error"`
	ErrorDetail *struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

func (app Yacu) PullImage(ctx context.Context, repository reference.NamedTagged) error {
	logger := zerolog.Ctx(ctx)

	ctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()

	pullOptions := image.PullOptions{}
	if authEntry := app.Registries.GetAuthConfigFor(reference.Domain(repository)); authEntry != nil {
		auth, err := registry.EncodeAuthConfig(
			registry.AuthConfig{
				Username: authEntry.Username,
				Password: authEntry.Password,
			},
		)

		if err != nil {
			logger.Err(err).Msg("Encoding AuthConfig failed")
			return fmt.Errorf("encoding authentication configuration failed: %w", err)
		}

		pullOptions.RegistryAuth = auth
	}

	response, err := app.Client.ImagePull(
		ctx,
		repository.String(),
		pullOptions,
	)

	if err != nil {
		logger.Err(err).Msg("Failed to pull image")
		return fmt.Errorf("failed to pull image: %w", err)
	}

	defer response.Close()

	decoder := json.NewDecoder(response)
	for {
		var msg pullMessage
		if err := decoder.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			logger.Err(err).Msg("Failure while pulling image")
			return fmt.Errorf("failure while pulling image: %w", err)
		}

		if msg.ErrorDetail != nil && len(msg.ErrorDetail.Message) > 0 {
			msg.Error = msg.ErrorDetail.Message
		}
		if len(msg.Error) > 0 {
			logger.Error().Str("error", msg.Error).Msg("Failure while pulling image")
			return fmt.Errorf("failure while pulling image: %s", msg.Error)
		}
	}
}

// Finds the running containers that declare a compose depends_on on the given container.
//
// Compose records dependencies by service name, so for compose containers the
// service name is matched within the same project or otherwise the container name is matched.
func (app Yacu) GetDependingContainers(ctx context.Context, dependsOn *container.InspectResponse) (yacucontainer.DependantContainers, error) {
	logger := zerolog.Ctx(ctx)
	logger.Debug().Msg("Fetching depending containers")

	target := strings.TrimPrefix(dependsOn.Name, "/")
	listFilters := filters.NewArgs(
		filters.KeyValuePair{
			Key: "label", Value: yacucontainer.LABEL_DEPENDS_ON,
		},
		filters.KeyValuePair{
			Key: "status", Value: "running",
		},
	)

	project := dependsOn.Config.Labels[yacucontainer.LABEL_PROJECT]
	service := dependsOn.Config.Labels[yacucontainer.LABEL_SERVICE]
	if len(project) > 0 && len(service) > 0 {
		target = service
		listFilters.Add("label", yacucontainer.LABEL_PROJECT+"="+project)
	}

	// list all containers with the compose label
	containers, err := app.Client.ContainerList(
		ctx,
		container.ListOptions{Filters: listFilters},
	)
	if err != nil {
		logger.Err(err).Msg("ContainerList request failed")
		return nil, fmt.Errorf("listing containers failed: %w", err)
	}

	dependantContainers := yacucontainer.DependantContainers{}
	for i := range containers {
		c := &containers[i]

		// no need to check if it exists as it is filtered that way
		val := c.Labels[yacucontainer.LABEL_DEPENDS_ON]
		// skip if value is empty
		if len(val) == 0 {
			continue
		}

		// format: service[:condition[:restart]],...
		for _, value := range strings.Split(val, ",") {
			depVals := strings.Split(value, ":")

			dependency := depVals[0]
			if dependency != target {
				continue
			}

			condition := yacucontainer.DEPENDENCY_HEALTHY

			if len(depVals) > 1 {
				condStr := strings.ToLower(depVals[1])
				if condStr == "service_started" {
					condition = yacucontainer.DEPENDENCY_STARTED
				} else if condStr == "service_completed_successfully" {
					condition = yacucontainer.DEPENDENCY_COMPLETED
				} // else DEPENDENCY_HEALTHY

				if len(depVals) > 2 {
					restart, err := strconv.ParseBool(depVals[2])
					// can recreate without restarting this container if false
					if err == nil && !restart {
						break
					}
				}
			}

			dependant := yacucontainer.NewDependant(c, app.Updater.StopTimeout, target, condition)
			dependantContainers = append(dependantContainers, dependant)
			// can be stopped as there cannot be multiple instances of the same dependency
			break
		}
	}
	return dependantContainers, nil
}

func (app Yacu) RemoveUnusedImages(ctx context.Context, images ...*yacuimage.ImageData) (count int) {
	logger := zerolog.Ctx(ctx)

	// include stopped containers, their images are still in use
	containers, err := app.Client.ContainerList(
		ctx, container.ListOptions{All: true},
	)

	if err != nil {
		logger.Err(err).Msg("ContainerList request failed")
		return
	}

	for _, img := range images {

		removeImage := true
		for _, container := range containers {
			if utils.IdEncoded(container.ImageID) == utils.IdEncoded(img.ID) {
				removeImage = false
				break
			}
		}

		if removeImage {
			imageLogger := logger.With().Str("id", img.ID).Logger()
			imageCtx := imageLogger.WithContext(ctx)

			imageLogger.Debug().Msg("Removing unused image")

			response, err := app.Client.ImageRemove(
				ctx,
				img.ID,
				image.RemoveOptions{
					Force: true,
				},
			)

			if err != nil {
				imageLogger.Err(err).Msg("Removing image failed")
				app.Webhooks.ImageRemovalFailed(imageCtx, img, err)
			} else {
				count += 1
				imageLogger.Debug().Str("response", fmt.Sprintf("%v", response)).Msg("Unused image removed")
			}
		}
	}
	return
}
