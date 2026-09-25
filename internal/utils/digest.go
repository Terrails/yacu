package utils

import (
	"strings"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/image"
	"github.com/opencontainers/go-digest"
	yacutypes "github.com/terrails/yacu/internal/types"
)

func GetRepoDigest(repository reference.Named, image *image.InspectResponse) (*digest.Digest, error) {
	familiarName := reference.FamiliarName(repository)

	for _, str := range image.RepoDigests {
		split := strings.Split(str, "@")

		if len(split) > 1 && split[0] == familiarName {
			digest, err := digest.Parse(split[1])
			if err != nil {
				return nil, err
			}
			return &digest, nil
		}
	}
	return nil, yacutypes.ErrMissingRepoDigest
}
