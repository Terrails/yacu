package types

import "errors"

var (
	ErrRepositoryNotTagged = errors.New("repository is not tagged")
	ErrRepositoryPinned    = errors.New("repository is pinned to a digest")
	ErrInvalidReference    = errors.New("image reference cannot be parsed")
	ErrMissingRepoDigest   = errors.New("image is missing a repository digest")
)

// whether err means the container's image can never be updated by yacu (local build, digest reference, pinned tag),
// as opposed to a failure that may succeed on a later attempt.
func IsUnsupportedImage(err error) bool {
	return errors.Is(err, ErrRepositoryNotTagged) ||
		errors.Is(err, ErrRepositoryPinned) ||
		errors.Is(err, ErrInvalidReference) ||
		errors.Is(err, ErrMissingRepoDigest)
}
