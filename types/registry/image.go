package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/image"
	"github.com/containers/image/v5/manifest"
	"github.com/docker/distribution/reference"
	"github.com/opencontainers/go-digest"
	"github.com/rs/zerolog"
	"github.com/terrails/yacu/types/config"
)

type ImageData struct {
	Name    string
	Tag     string
	Created *time.Time
	Digest  digest.Digest
	Arch    string
	OS      string
}

func GetImageDataFromRegistry(ctx context.Context, entries *config.RegistryEntries, named reference.Named) (*ImageData, error) {
	logger := zerolog.Ctx(ctx)

	ref, err := docker.NewReference(named)
	if err != nil {
		logger.Err(err).Msg("parsing image name failed")
		return nil, fmt.Errorf("parsing image name failed: %w", err)
	}

	domain := reference.Domain(named)
	sysCtx := entries.GetSystemContextFor(domain)

	src, err := ref.NewImageSource(context.Background(), sysCtx)
	if err != nil {
		logger.Err(err).Msg("fetching image source failed")
		return nil, fmt.Errorf("fetching image source failed: %w", err)
	}
	defer src.Close()

	img, err := image.FromUnparsedImage(context.Background(), sysCtx, image.UnparsedInstance(src, nil))
	if err != nil {
		logger.Err(err).Msg("fetching image failed")
		return nil, fmt.Errorf("fetching image failed: %w", err)
	}

	imgData, err := img.Inspect(context.Background())
	if err != nil {
		logger.Err(err).Msg("image inspect call failed")
		return nil, fmt.Errorf("image inspect call failed: %w", err)
	}

	rawManifest, _, err := src.GetManifest(context.Background(), nil)
	if err != nil {
		logger.Err(err).Msg("fetching image manifest failed")
		return nil, fmt.Errorf("fetching image manifest failed: %w", err)
	}

	digest, err := manifest.Digest(rawManifest)
	if err != nil {
		logger.Err(err).Msg("fetching image digest failed")
		return nil, fmt.Errorf("fetching image digest failed: %w", err)
	}

	parsedData := ImageData{
		Name:    img.Reference().DockerReference().Name(),
		Tag:     imgData.Tag,
		Created: imgData.Created,
		Digest:  digest,
		Arch:    imgData.Architecture,
		OS:      imgData.Os,
	}

	return &parsedData, nil
}
