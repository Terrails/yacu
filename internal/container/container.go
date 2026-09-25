package container

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/container"
	"github.com/opencontainers/go-digest"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/internal/docker"
	"github.com/terrails/yacu/internal/image"
	"github.com/terrails/yacu/internal/utils"

	yacutypes "github.com/terrails/yacu/internal/types"
)

type Container struct {
	Raw    *container.InspectResponse
	ID     string
	Name   string
	Labels map[string]string

	Image       *image.ImageData
	Repository  reference.NamedTagged
	StopTimeout int
	MinImageAge int
}

type Containers []*Container

func New(ctx context.Context, api docker.API, data *container.InspectResponse, stopTimeout, minImageAge int) (*Container, error) {
	named, err := reference.ParseNormalizedNamed(data.Config.Image)
	if err != nil {
		// e.g. a container created from a bare image ID
		return nil, fmt.Errorf("%w: %w", yacutypes.ErrInvalidReference, err)
	}
	if _, ok := named.(reference.Digested); ok {
		// pulling by tag would silently replace the pinned image
		return nil, yacutypes.ErrRepositoryPinned
	}
	named = reference.TagNameOnly(named)

	namedTagged, ok := named.(reference.NamedTagged)
	// only allow tagged images
	if !ok {
		return nil, yacutypes.ErrRepositoryNotTagged
	}

	imageRaw, err := api.ImageInspect(ctx, data.Image)
	if err != nil {
		return nil, err
	}

	imageData, err := image.NewData(&imageRaw, namedTagged)
	if err != nil {
		return nil, err
	}

	if val, ok := data.Config.Labels[LABEL_STOP_TIMEOUT]; ok {
		if ival, err := strconv.ParseInt(val, 10, 0); err == nil {
			stopTimeout = int(ival)
		}
	}

	if val, ok := data.Config.Labels[LABEL_IMAGE_AGE]; ok {
		if ival, err := strconv.ParseInt(val, 10, 0); err == nil {
			minImageAge = int(ival)
		}
	}

	return &Container{
		Raw:         data,
		ID:          data.ID,
		Name:        data.Name,
		Labels:      data.Config.Labels,
		Image:       imageData,
		Repository:  namedTagged,
		StopTimeout: stopTimeout,
		MinImageAge: minImageAge,
	}, nil
}

func (c *Container) IsRunning() bool {
	return c.Raw.State.Status == container.StateRunning
}

func (c *Container) IsYacu() bool {
	return strings.HasPrefix(reference.Path(c.Repository), "terrails/yacu")
}

func (c *Container) RepositoryFamiliarized() string {
	return utils.FamiliarTagged(c.Repository)
}

func (c *Container) HasRepoDigest(digest digest.Digest) bool {
	for _, str := range c.Image.Raw.RepoDigests {
		if strings.Contains(str, digest.String()) {
			return true
		}
	}
	return false
}

func (c *Container) CleanImageId() string {
	// image ID without hash (sha256:) portion
	split := strings.SplitN(c.Image.ID, ":", 2)

	if len(split) == 1 {
		return c.Image.ID
	} else {
		return split[1]
	}
}

func ShouldScan(summary *container.Summary, all, stopped bool) bool {
	if !stopped && summary.State != container.StateRunning {
		return false
	}

	if val, ok := summary.Labels[LABEL_ENABLE]; ok {
		if bval, err := strconv.ParseBool(val); err == nil {
			return bval
		} else {
			// do not scan if label value is invalid
			return false
		}
	}

	return all
}

func (c *Container) IsOutdated() (bool, error) {
	// parse created time from container image
	createdTime := c.Image.Created

	if utils.DaysPassed(createdTime) < c.MinImageAge {
		return false, nil
	}

	return true, nil
}

func (c *Container) Stop(ctx context.Context, api docker.API) error {
	logger := c.logger(ctx)
	logger.Debug().Msg("Attempting to stop container")

	if err := api.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &c.StopTimeout}); err != nil {
		logger.Err(err).Msg("Failed to stop container")
		return fmt.Errorf("failed to stop container %s: %w", c.Name, err)
	}
	logger.Debug().Msg("Stopped container")
	return nil
}

func (c *Container) Start(ctx context.Context, api docker.API) error {
	logger := c.logger(ctx)
	logger.Debug().Msg("Attempting to start container")

	// used to avoid odd `failed to create shim task` entrypoint errors when starting a newly created container
	for i := 0; i < 3; i++ {
		// do max 3 retries, if it fails after then its just broken
		if err := api.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
			if i == 2 {
				logger.Err(err).Msg("Failed to start container")
				return fmt.Errorf("failed to start container %s: %w", c.Name, err)
			}
			time.Sleep(time.Second)
			continue
		}
		break
	}

	return nil
}

func (c *Container) logger(ctx context.Context) *zerolog.Logger {
	logger := zerolog.Ctx(ctx).With().Str("container", c.Name).Str("repository", c.Repository.String()).Logger()
	return &logger
}
