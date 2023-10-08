package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/types/config"
	"github.com/terrails/yacu/types/database"
	"github.com/terrails/yacu/types/image"
	"github.com/terrails/yacu/types/set"
	"github.com/terrails/yacu/types/webhook"
	"github.com/terrails/yacu/utils"
	"golang.org/x/exp/maps"

	yacutypes "github.com/terrails/yacu/types"
	yacucontainer "github.com/terrails/yacu/types/container"
	yacuregistry "github.com/terrails/yacu/types/registry"
)

type Yacu struct {
	Client   *client.Client
	Webhooks *webhook.Webhooks

	DB         database.Database
	Scanner    config.Scanner
	Updater    config.Updater
	Registries config.RegistryEntries
}

func (app Yacu) Run(ctx context.Context) {
	logger := zerolog.Ctx(ctx)

	containers, err := app.FetchUpdates(ctx)
	if err != nil {
		app.Webhooks.Error(ctx, "Unable to fetch updates", err)
		return
	}

	if len(containers) == 0 {
		logger.Info().Msg("No new updates found")
	} else {
		logger.Info().Int("count", len(containers)).Msg("Found new updates")
	}

	// pull all new images at once
	for _, container := range containers {
		imageLogger := logger.With().Str("service", "image_pull").Str("image", container.RepositoryFamiliarized()).Logger()
		imageCtx := imageLogger.WithContext(context.Background())

		// check if image has already been pulled in case that multiple containers with the same image are being updated
		if yes, err := app.IsLatestImagePresent(imageCtx, container.Repository); err != nil {
			app.Webhooks.ImageError(imageCtx, container.Image, "Unable to check if image is latest", err)
			return
		} else if !yes {
			imageLogger.Debug().Msg("Pulling image")

			if err = app.PullImage(imageCtx, container.Repository); err != nil {
				app.Webhooks.ImageError(imageCtx, container.Image, "Unable to pull image", err)
				return
			}

			newImageRaw, _, err := app.Client.ImageInspectWithRaw(context.Background(), container.Repository.String())
			if err != nil {
				imageLogger.Err(err).Msg("ImageInspect request failed")
				app.Webhooks.ImageError(imageCtx, container.Image, "Unable to inspect image", err)
				return
			}

			newImageData, err := image.NewData(&newImageRaw, container.Repository)
			if err != nil {
				app.Webhooks.ImageError(imageCtx, container.Image, "Unable to initialize image", err)
				return
			}

			imageLogger.Info().Msg("Pulled image")
			app.Webhooks.ImageUpdated(imageCtx, container.Image, newImageData)
		}
	}

	successCount := 0
	imgToRemove := set.NewImageSet()

	// update all containers
	for _, container := range containers {
		containerLogger := logger.With().Str("service", "container_update").Str("container", container.Name).Str("image", container.RepositoryFamiliarized()).Logger()
		containerCtx := containerLogger.WithContext(context.Background())

		containerLogger.Debug().Msg("Updating container")

		var updateWarnings []string = []string{}

		shouldRestart := container.IsRunning()
		var dependantContainers yacucontainer.DependantContainers = nil

		if shouldRestart {
			dependantContainers, err := app.GetDependingContainers(containerCtx, container.Raw)
			if err != nil {
				app.Webhooks.ContainerError(containerCtx, container, "Unable to fetch depending containers", err)
				continue
			}

			updateWarnings = append(updateWarnings, dependantContainers.Stop(containerCtx, app.Client)...)
			if len(updateWarnings) > 0 {
				containerLogger.Warn().Str("warnings", fmt.Sprintf("%v", updateWarnings)).Msg("Received warnings while stopping depending containers")
			}

			if err = container.Stop(containerCtx, app.Client); err != nil {
				app.Webhooks.ContainerError(containerCtx, container, "Unable to stop container", err)
				continue
			}
		}

		var singleNetSettings network.NetworkingConfig = network.NetworkingConfig{}
		for netName, netSettings := range container.Raw.NetworkSettings.Networks {
			singleNetSettings.EndpointsConfig = map[string]*network.EndpointSettings{
				netName: netSettings,
			}
			break
		}

		containerLogger.Debug().Msg("Removing container")
		if err := app.Client.ContainerRemove(
			context.Background(),
			container.ID,
			types.ContainerRemoveOptions{
				Force:         true,
				RemoveVolumes: app.Updater.RemoveVolumes,
			},
		); err != nil {
			containerLogger.Err(err).Msg("Failed to remove container")
			app.Webhooks.ContainerError(containerCtx, container, "Unable to remove container", err)
			continue
		}

		containerLogger.Debug().Msg("Creating container")
		response, err := app.Client.ContainerCreate(
			context.Background(),
			container.Raw.Config,
			container.Raw.HostConfig,
			&singleNetSettings,
			nil,
			container.Raw.Name,
		)

		if err != nil {
			containerLogger.Err(err).Msg("Failed to create container")
			app.Webhooks.ContainerError(containerCtx, container, "Unable to create container", err)
			continue
		}

		newId := response.ID
		if len(response.Warnings) > 0 {
			updateWarnings = append(updateWarnings, response.Warnings...)
			containerLogger.Warn().Str("warnings", fmt.Sprintf("%v", response.Warnings)).Msg("Received warnings while creating container")
		}

		// cannot use multiple networks if host networking is enabled
		if !container.Raw.HostConfig.NetworkMode.IsHost() {

			// should be already connected to 1 network
			if len(container.Raw.NetworkSettings.Networks) > 1 {
				containerLogger.Debug().Msg("Connecting container to networks")
			}

			// Add other networks
			for netName, netSettings := range container.Raw.NetworkSettings.Networks {

				// skip already connected
				if _, ok := singleNetSettings.EndpointsConfig[netName]; ok {
					continue
				}

				containerLogger.Debug().Str("network", netName).Msg("Connecting container to network")

				if err := app.Client.NetworkConnect(
					context.Background(),
					netName,
					newId,
					netSettings,
				); err != nil {
					containerLogger.Err(err).Str("network", netName).Msg("Connecting to network failed")
					// since we already came this far, might as well do everything and send a warning about it
					updateWarnings = append(updateWarnings, fmt.Sprintf("connecting to network %s failed: %v", netName, err))
				}
			}
		}

		newData, err := app.Client.ContainerInspect(context.Background(), newId)
		if err != nil {
			containerLogger.Err(err).Str("id", newId).Msg("ContainerInspect request failed")
			app.Webhooks.ContainerError(containerCtx, container, "Unable to inspect container", err)
			continue
		}

		newContainer, err := yacucontainer.New(app.Client, &newData, app.Updater.StopTimeout, app.Scanner.ImageAge)
		if err != nil {
			containerLogger.Err(err).Str("container", newData.Name).Msg("Initializing recreated container failed")
			app.Webhooks.ContainerError(containerCtx, container, "Unable to initialize container", err)
			continue
		}

		if shouldRestart {
			if err = newContainer.Start(containerCtx, app.Client); err != nil {
				app.Webhooks.ContainerError(containerCtx, container, "Unable to start container", err)
				continue
			}

			warnings := dependantContainers.Start(containerCtx, app.Client)
			if len(warnings) > 0 {
				updateWarnings = append(updateWarnings, warnings...)
				containerLogger.Warn().Str("warnings", fmt.Sprintf("%v", warnings)).Msg("Received warnings while starting depending containers")
			}
		}

		successCount += 1
		imgToRemove.Add(container.Image)
		containerLogger.Info().Msg("Updated container")
		app.Webhooks.ContainerUpdated(containerCtx, container, newContainer, updateWarnings...)
	}

	logger.Info().Int("total", len(containers)).Int("successful", successCount).Msg("Container updates completed")

	if app.Updater.RemoveImages && len(imgToRemove.Items) > 0 {
		logger.Debug().Int("count", len(imgToRemove.Items)).Msg("Removing unused images")
		count := app.RemoveUnusedImages(ctx, maps.Values(imgToRemove.Items)...)
		logger.Info().Int("count", count).Msg("Removed unused images")
	}
}

func (app Yacu) FetchUpdates(ctx context.Context) (yacucontainer.Containers, error) {
	logger := zerolog.Ctx(ctx).With().Str("service", "scanner").Logger()
	ctx = logger.WithContext(context.Background())

	// List all containers
	cntList, err := app.Client.ContainerList(
		context.Background(),
		types.ContainerListOptions{},
	)

	if err != nil {
		logger.Err(err).Msg("ContainerList request failed")
		return nil, fmt.Errorf("listing containers failed: %w", err)
	}

	containers := yacucontainer.Containers{}
	for _, c := range cntList {
		// fetch detailed info
		ci, err := app.Client.ContainerInspect(context.Background(), c.ID)
		if err != nil {
			logger.Err(err).Str("id", c.ID).Msg("ContainerInspect request failed")
			return nil, fmt.Errorf("inspecting container %s failed: %w", c.ID, err)
		}

		container, err := yacucontainer.New(app.Client, &ci, app.Updater.StopTimeout, app.Scanner.ImageAge)
		if err != nil {
			if errors.Is(err, yacutypes.ErrRepositoryNotTagged) {
				// skip over any repositories that use digests as there are no updates for those
				continue
			} else {
				logger.Err(err).Str("container", ci.Name).Msg("Container initialization failed")
				return nil, fmt.Errorf("initializing container %s failed: %w", ci.Name, err)
			}
		}

		// checks labels and config related flags
		if !container.ShouldScan(app.Scanner.ScanAll, app.Scanner.ScanStopped) {
			continue
		}

		if yes, err := container.IsOutdated(); err != nil {
			return nil, err
		} else if yes {
			if yes, err = app.IsRemotePullable(ctx, container); err != nil {
				return nil, err
			} else if yes {
				containers = append(containers, container)
			}
		}
	}

	return containers, nil
}

func (app Yacu) IsRemotePullable(ctx context.Context, container *yacucontainer.Container) (bool, error) {
	logger := zerolog.Ctx(ctx).With().
		Str("container", container.Name).
		Str("image", container.RepositoryFamiliarized()).
		Logger()
	ctx = logger.WithContext(context.Background())

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

	currentImgData, _, err := app.Client.ImageInspectWithRaw(context.Background(), named.String())
	if err != nil {
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

func (app Yacu) PullImage(ctx context.Context, repository reference.NamedTagged) error {
	logger := zerolog.Ctx(ctx)

	pullOptions := types.ImagePullOptions{}
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
		context.Background(),
		repository.String(),
		pullOptions,
	)

	if err != nil {
		logger.Err(err).Msg("Failed to pull image")
		return fmt.Errorf("failed to pull image: %w", err)
	}

	defer response.Close()

	if _, err := io.ReadAll(response); err != nil {
		logger.Err(err).Msg("Failure while pulling image")
		return fmt.Errorf("failure while pulling image: %w", err)
	}

	return nil
}

func (app Yacu) GetDependingContainers(ctx context.Context, dependsOn *types.ContainerJSON) (yacucontainer.DependantContainers, error) {
	logger := zerolog.Ctx(ctx)
	logger.Debug().Msg("Fetching depending containers")

	// list all containers with the compose label
	containers, err := app.Client.ContainerList(
		context.Background(),
		types.ContainerListOptions{
			Filters: filters.NewArgs(
				filters.KeyValuePair{
					Key: "label", Value: yacucontainer.LABEL_DEPENDS_ON,
				},
				filters.KeyValuePair{
					Key: "status", Value: "running",
				},
			),
		},
	)
	if err != nil {
		logger.Err(err).Msg("ContainerList request failed")
		return nil, fmt.Errorf("listing containers failed: %w", err)
	}

	dependantContainers := yacucontainer.DependantContainers{}
	for _, container := range containers {
		// no need to check if it exists as it is filtered that way
		val := container.Labels[yacucontainer.LABEL_DEPENDS_ON]
		// skip if value is empty
		if len(val) == 0 {
			continue
		}

		for _, value := range strings.Split(val, ",") {
			depVals := strings.Split(value, ":")

			dependency := depVals[0]
			if dependency != dependsOn.Name[1:] {
				continue
			}

			condition := yacucontainer.DEPENDENCY_HEALTHY
			restart := true

			if len(depVals) > 1 {
				condStr := strings.ToLower(depVals[1])
				if condStr == "service_started" {
					condition = yacucontainer.DEPENDENCY_STARTED
				} else if condStr == "service_completed_successfully" {
					condition = yacucontainer.DEPENDENCY_COMPLETED
				} // else DEPENDENCY_HEALTHY

				if len(depVals) > 2 {
					restart, err = strconv.ParseBool(depVals[2])
					// can recreate without restarting this container if false
					if err == nil && !restart {
						continue
					}
				}
			}

			container := yacucontainer.NewDependant(&container, app.Updater.StopTimeout, dependsOn, condition)
			dependantContainers = append(dependantContainers, container)
			// can be stopped as there cannot be multiple instances of the same dependency
			break
		}
	}
	return dependantContainers, nil
}

func (app Yacu) RemoveUnusedImages(ctx context.Context, images ...*image.ImageData) (count int) {
	logger := zerolog.Ctx(ctx)

	containers, err := app.Client.ContainerList(
		context.Background(), types.ContainerListOptions{},
	)

	if err != nil {
		logger.Err(err).Msg("ContainerList request failed")
		return
	}

	for _, image := range images {

		removeImage := true
		for _, container := range containers {
			if utils.IdEncoded(container.ImageID) == utils.IdEncoded(image.ID) {
				removeImage = false
				break
			}
		}

		if removeImage {
			imageLogger := logger.With().Str("id", image.ID).Logger()
			imageCtx := imageLogger.WithContext(context.Background())

			imageLogger.Debug().Msg("Removing unused image")

			response, err := app.Client.ImageRemove(
				context.Background(),
				image.ID,
				types.ImageRemoveOptions{
					Force: true,
				},
			)

			if err != nil {
				imageLogger.Err(err).Msg("Removing image failed")
				app.Webhooks.ImageRemovalFailed(imageCtx, image, err)
			} else {
				count += 1
				imageLogger.Debug().Str("response", fmt.Sprintf("%v", response)).Msg("Unused image removed")
			}
		}
	}
	return
}
