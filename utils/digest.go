package utils

import (
	"strings"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/api/types"
	"github.com/opencontainers/go-digest"
	yacutypes "github.com/terrails/yacu/types"
)

func GetRepoDigest(repository reference.Named, image *types.ImageInspect) (*digest.Digest, error) {
	familiarName := reference.FamiliarName(repository)

	for _, str := range image.RepoDigests {
		split := strings.Split(str, "@")

		if len(split) > 1 && split[0] == familiarName {
			digest := digest.FromString(split[1])
			return &digest, nil
		}
	}
	return nil, yacutypes.ErrMissingRepoDigest
}
