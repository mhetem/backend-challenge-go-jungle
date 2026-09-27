package app

import (
	"time"

	"github.com/google/uuid"
)

type Clock func() time.Time

type IDs func() uuid.UUID

func SystemClock() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func NewID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}
