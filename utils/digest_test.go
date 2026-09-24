package utils

import (
	"errors"
	"testing"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/image"
	yacutypes "github.com/terrails/yacu/types"
)

func TestGetRepoDigest(t *testing.T) {
	const want = "sha256:0d17b565c37bcbd895e9d92315a05c1c3c9a29f762b011a10c54a66cd53c9b31"
	named, _ := reference.ParseNormalizedNamed("nginx:latest")

	got, err := GetRepoDigest(named, &image.InspectResponse{
		RepoDigests: []string{"other/image@sha256:" + "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "nginx@" + want},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestGetRepoDigestMissing(t *testing.T) {
	named, _ := reference.ParseNormalizedNamed("local/build:latest")

	if _, err := GetRepoDigest(named, &image.InspectResponse{}); !errors.Is(err, yacutypes.ErrMissingRepoDigest) {
		t.Fatalf("expected ErrMissingRepoDigest, got %v", err)
	}
}
