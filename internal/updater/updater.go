package updater

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/internal/config"
	"github.com/terrails/yacu/internal/database"
	"github.com/terrails/yacu/internal/docker"
	yacuimage "github.com/terrails/yacu/internal/image"
	"github.com/terrails/yacu/internal/set"
	"github.com/terrails/yacu/internal/utils"
	"github.com/terrails/yacu/internal/webhook"

	yacucontainer "github.com/terrails/yacu/internal/container"
	yacuregistry "github.com/terrails/yacu/internal/registry"
)

type Yacu struct {
	Client   docker.API
	Webhooks *webhook.Webhooks

	DB         database.Database
	Scanner    config.Scanner
	Updater    config.Updater
	Registries config.RegistryEntries

	// looks up images in their registry, GetImageDataFromRegistry when nil
	RegistryLookup func(ctx context.Context, entries *config.RegistryEntries, named reference.Named) (*yacuregistry.ImageData, error)
}

// A failed step of an image or container update. Context names the step that failed.
type updateError struct {
	Context string
	Err     error
}

func (e *updateError) Error() string { return fmt.Sprintf("%s: %v", e.Context, e.Err) }

func (e *updateError) Unwrap() error { return e.Err }

// Checks for updates and applies them, returning how many containers failed to
// be checked or updated.
func (app Yacu) Run(ctx context.Context) (failures int) {
	logger := zerolog.Ctx(ctx)

	containers, errs := app.FetchUpdates(ctx)
	if ctx.Err() != nil {
		// errors caused by the interruption are not worth reporting
		logger.Info().Msg("Shutting down, scan interrupted")
		return 0
	}

	if len(errs) > 0 {
		logger.Warn().Errs("errors", errs).Msg("Failed to fetch some updates")
		for _, err := range errs {
			app.Webhooks.Error(ctx, "Unable to fetch updates", err)
		}

		if app.Scanner.FailOnError {
			logger.Error().Msg("Not applying updates because scanning failed (scanner.fail_on_error)")
			return len(errs)
		}
	}

	if containers == nil {
		return len(errs)
	}

	if len(containers) == 0 {
		logger.Info().Msg("No new updates found")
	} else {
		logger.Info().Int("count", len(containers)).Msg("Found new updates")
	}

	return len(errs) + app.ApplyUpdates(ctx, containers)
}

// Pulls the new images and recreates the given containers, returning how many were not updated.
// A container is skipped when its image could not be pulled.
// On shutdown (ctx cancelled) an update in progress is completed, the remaining ones are skipped.
func (app Yacu) ApplyUpdates(ctx context.Context, containers yacucontainer.Containers) (failures int) {
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
		count := app.RemoveUnusedImages(ctx, slices.Collect(maps.Values(imgToRemove.Items))...)
		logger.Info().Int("count", count).Msg("Removed unused images")
	}
	return len(containers) - successCount
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
