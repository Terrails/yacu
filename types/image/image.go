package image

import (
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/image"
	"github.com/opencontainers/go-digest"
	"github.com/terrails/yacu/utils"
)

type ImageData struct {
	Raw *image.InspectResponse

	ID         string
	Created    time.Time
	Repository reference.NamedTagged
	RepoDigest digest.Digest
}

func NewData(img *image.InspectResponse, repository reference.NamedTagged) (*ImageData, error) {
	digest, err := utils.GetRepoDigest(repository, img)
	if err != nil {
		return nil, err
	}

	createdTime, err := time.Parse(time.RFC3339Nano, img.Created)
	if err != nil {
		return nil, err
	}

	return &ImageData{
		Raw:        img,
		ID:         img.ID,
		Created:    createdTime,
		Repository: repository,
		RepoDigest: *digest,
	}, nil
}
