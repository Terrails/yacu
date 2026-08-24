package utils

import (
	"fmt"

	"github.com/distribution/reference"
)

func FamiliarTagged(repository reference.NamedTagged) string {
	return fmt.Sprintf("%s:%s", reference.FamiliarName(repository), repository.Tag())
}
