package app

import (
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
)

var (
	ErrTransient         = errors.New("transient failure")
	ErrRetryableConflict = errors.New("retryable conflict")
	ErrPermanent         = errors.New("permanent failure")
	ErrConcurrentUpdate  = fmt.Errorf("%w: row changed concurrently", ErrRetryableConflict)
)

var (
	ErrIdempotencyKeyReused = &domain.Error{Code: "IDEMPOTENCY_KEY_REUSED", Category: domain.Conflict}
	ErrExternalIDConflict   = &domain.Error{Code: "EXTERNAL_TRANSACTION_ID_CONFLICT", Category: domain.Conflict}
)

type WalletExistsError struct {
	WalletID uuid.UUID
}

func (e *WalletExistsError) Error() string {
	return fmt.Sprintf("%s: wallet %s", domain.ErrWalletExists.Code, e.WalletID)
}

func (e *WalletExistsError) Unwrap() error {
	return domain.ErrWalletExists
}
