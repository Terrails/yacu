package utils

import (
	"strings"
)

// the first 12 characters of an ID without its algorithm, as docker shows them
func ShortId(longId string) string {
	id := IdEncoded(longId)
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// returns an id without algorithm part
func IdEncoded(id string) string {
	if strings.Contains(id, ":") {
		return strings.Split(id, ":")[1]
	} else {
		return id
	}
}
