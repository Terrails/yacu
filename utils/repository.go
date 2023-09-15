package utils

import (
	"fmt"

	"github.com/docker/distribution/reference"
)

func FamiliarTagged(repository reference.NamedTagged) string {
	return fmt.Sprintf("%s:%s", reference.FamiliarName(repository), repository.Tag())
}
