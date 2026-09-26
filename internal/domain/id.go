package domain

import (
	"strings"

	"github.com/google/uuid"
)

var namespace = uuid.MustParse("5d3c8b2e-7f41-4a96-b0e3-9c2d6a1f8e47")

func DeriveID(parts ...string) uuid.UUID {
	return uuid.NewSHA1(namespace, []byte(strings.Join(parts, "/")))
}
