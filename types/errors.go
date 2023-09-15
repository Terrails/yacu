package types

import "errors"

var (
	ErrRepositoryNotTagged = errors.New("repository is not tagged")
	ErrMissingRepoDigest   = errors.New("image is missing a repository digest")
)
