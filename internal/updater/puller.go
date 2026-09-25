package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/rs/zerolog"
	yacuimage "github.com/terrails/yacu/internal/image"
	"github.com/terrails/yacu/internal/utils"

	yacucontainer "github.com/terrails/yacu/internal/container"
)

// how long pulling a single image may take
const pullTimeout = 30 * time.Minute

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

	// the daemon does not read any client configuration, credentials are passed along with the pull
	credentials, err := app.Registries.GetCredentials(repository)
	if err != nil {
		logger.Err(err).Msg("Resolving registry credentials failed")
		return err
	}

	pullOptions := image.PullOptions{}
	if len(credentials.Username) > 0 || len(credentials.Password) > 0 || len(credentials.IdentityToken) > 0 {
		auth, err := registry.EncodeAuthConfig(
			registry.AuthConfig{
				Username:      credentials.Username,
				Password:      credentials.Password,
				IdentityToken: credentials.IdentityToken,
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
