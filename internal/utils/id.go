package utils

import (
	"strings"
)

func ShortId(longId string) string {
	if strings.Contains(longId, ":") {
		return strings.Split(longId, ":")[1][:12]
	} else {
		return longId[:12]
	}
}

// returns an id without algorithm part
func IdEncoded(id string) string {
	if strings.Contains(id, ":") {
		return strings.Split(id, ":")[1]
	} else {
		return id
	}
}
