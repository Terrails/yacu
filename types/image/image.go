package image

import (
	"time"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/api/types"
	"github.com/opencontainers/go-digest"
	"github.com/terrails/yacu/utils"
)

type ImageData struct {
	Raw *types.ImageInspect

	ID         string
	Created    time.Time
	Repository reference.NamedTagged
	RepoDigest digest.Digest
}

func NewData(image *types.ImageInspect, repository reference.NamedTagged) (*ImageData, error) {
	digest, err := utils.GetRepoDigest(repository, image)
	if err != nil {
		return nil, err
	}

	createdTime, err := time.Parse(time.RFC3339Nano, image.Created)
	if err != nil {
		return nil, err
	}

	return &ImageData{
		Raw:        image,
		ID:         image.ID,
		Created:    createdTime,
		Repository: repository,
		RepoDigest: *digest,
	}, nil
}
