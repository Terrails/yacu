package updater

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/container"
	"github.com/opencontainers/go-digest"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/internal/utils"

	yacucontainer "github.com/terrails/yacu/internal/container"
	yacuregistry "github.com/terrails/yacu/internal/registry"
	yacutypes "github.com/terrails/yacu/internal/types"
)

// how often checking a single container is attempted before giving up,
// and the base delay between attempts
const scanAttempts = 3

var scanRetryDelay = time.Second * 2

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

// Whether the registry has a newer image under the container's tag that is old enough to update to.
//
// What the registry was last found to have is stored and relied on for
// scanner.check_interval hours to rule an update out without querying it again.
// An update is always confirmed with the registry though, as pulling gets
// whatever the tag refers to by then.
func (app Yacu) IsRemotePullable(ctx context.Context, container *yacucontainer.Container) (bool, error) {
	logger := zerolog.Ctx(ctx).With().
		Str("container", container.Name).
		Str("image", container.RepositoryFamiliarized()).
		Logger()
	ctx = logger.WithContext(ctx)

	familiarNameTagged := container.RepositoryFamiliarized()

	// whether the registry's image is one to update to
	isUpdate := func(created time.Time, digest digest.Digest) bool {
		return utils.DaysPassed(created) >= container.MinImageAge && !container.HasRepoDigest(digest)
	}

	dbImage, err := app.DB.GetRemoteImageFromName(familiarNameTagged)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Err(err).Msg("Fetching remote image data from local database failed")
		return false, fmt.Errorf("fetching remote image data (%s) from local database failed: %w", familiarNameTagged, err)
	}

	checkInterval := time.Duration(app.Scanner.CheckInterval) * time.Hour
	if dbImage != nil && time.Since(dbImage.LastCheck) < checkInterval && !isUpdate(dbImage.Created, dbImage.Digest) {
		logger.Debug().Time("last_check", dbImage.LastCheck).Msg("Image up to date as of the last registry check")
		return false, nil
	}

	remoteData, err := app.lookupRemote(ctx, container.Repository)
	if err != nil {
		return false, err
	}

	if _, err := app.DB.SaveRemoteImage(
		familiarNameTagged,
		reference.Domain(container.Repository),
		*remoteData.Created,
		remoteData.Digest,
	); err != nil {
		logger.Err(err).Msg("Writing remote image data to local database failed")
		return false, fmt.Errorf("writing remote image data (%s) to local database failed: %w", familiarNameTagged, err)
	}

	if !isUpdate(*remoteData.Created, remoteData.Digest) {
		logger.Debug().Msg("Image up to date")
		return false, nil
	}

	logger.Debug().Msg("Image added to update queue")
	return true, nil
}

// the image the registry has under named's tag
func (app Yacu) lookupRemote(ctx context.Context, named reference.Named) (*yacuregistry.ImageData, error) {
	if app.RegistryLookup != nil {
		return app.RegistryLookup(ctx, &app.Registries, named)
	}
	return yacuregistry.GetImageDataFromRegistry(ctx, &app.Registries, named)
}
