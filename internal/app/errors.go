package app

import (
	"errors"
	"fmt"
)

var (
	ErrTransient         = errors.New("transient failure")
	ErrRetryableConflict = errors.New("retryable conflict")
	ErrPermanent         = errors.New("permanent failure")
	ErrConcurrentUpdate  = fmt.Errorf("%w: row changed concurrently", ErrRetryableConflict)
)
