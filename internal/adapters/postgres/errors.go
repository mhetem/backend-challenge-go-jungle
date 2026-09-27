package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
)

const (
	uniqueViolation      = "23505"
	idleInTransaction    = "25P03"
	serializationFailure = "40001"
	deadlockDetected     = "40P01"
	lockNotAvailable     = "55P03"
	queryCanceled        = "57014"
)

var uniqueConstraints = map[string]error{
	"wallets_player_currency_key":        domain.ErrWalletExists,
	"wager_transactions_external_id_key": app.ErrRetryableConflict,
	"wager_transactions_idempotency_key": app.ErrRetryableConflict,
	"wager_transactions_one_reversal":    app.ErrRetryableConflict,
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", kind(err), err)
}

func kind(err error) error {
	var pgErr *pgconn.PgError
	switch {
	case unreachable(err):
		return app.ErrTransient
	case !errors.As(err, &pgErr):
		return app.ErrPermanent
	}
	if mapped, ok := uniqueConstraints[pgErr.ConstraintName]; ok && pgErr.Code == uniqueViolation {
		return mapped
	}
	if transientCode(pgErr.Code) {
		return app.ErrTransient
	}
	return app.ErrPermanent
}

func unreachable(err error) bool {
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.As(err, &connectErr) || errors.As(err, &netErr) || pgconn.SafeToRetry(err)
}

func transientCode(code string) bool {
	switch code {
	case idleInTransaction, serializationFailure, deadlockDetected, lockNotAvailable, queryCanceled:
		return true
	}
	return strings.HasPrefix(code, "08") || strings.HasPrefix(code, "53") || strings.HasPrefix(code, "57P0")
}

func retryReason(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case serializationFailure:
			return "serialization"
		case deadlockDetected:
			return "deadlock"
		case lockNotAvailable:
			return "lock_timeout"
		}
	}
	if errors.Is(err, app.ErrRetryableConflict) {
		return "conflict"
	}
	return ""
}

func lookup(err, notFound error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound
	}
	return classify(err)
}

func corrupt(err error) error {
	return fmt.Errorf("%w: corrupt stored state: %w", app.ErrPermanent, err)
}
