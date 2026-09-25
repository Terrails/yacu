package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/image"
	"github.com/containers/image/v5/manifest"
	"github.com/distribution/reference"
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

// how long a single lookup of an image in its registry may take
const lookupTimeout = 2 * time.Minute

func GetImageDataFromRegistry(ctx context.Context, entries *config.RegistryEntries, named reference.Named) (*ImageData, error) {
	logger := zerolog.Ctx(ctx)

	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	ref, err := docker.NewReference(named)
	if err != nil {
		logger.Err(err).Msg("parsing image name failed")
		return nil, fmt.Errorf("parsing image name failed: %w", err)
	}

	sysCtx, err := entries.GetSystemContextFor(named)
	if err != nil {
		logger.Err(err).Msg("resolving registry credentials failed")
		return nil, err
	}

	src, err := ref.NewImageSource(ctx, sysCtx)
	if err != nil {
		logger.Err(err).Msg("fetching image source failed")
		return nil, fmt.Errorf("fetching image source failed: %w", err)
	}
	defer src.Close()

	img, err := image.FromUnparsedImage(ctx, sysCtx, image.UnparsedInstance(src, nil))
	if err != nil {
		logger.Err(err).Msg("fetching image failed")
		return nil, fmt.Errorf("fetching image failed: %w", err)
	}

	imgData, err := img.Inspect(ctx)
	if err != nil {
		logger.Err(err).Msg("image inspect call failed")
		return nil, fmt.Errorf("image inspect call failed: %w", err)
	}

	rawManifest, _, err := src.GetManifest(ctx, nil)
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
