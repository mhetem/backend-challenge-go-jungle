package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
)

func pgError(code, constraint string) error {
	return &pgconn.PgError{Code: code, ConstraintName: constraint}
}

func TestClassify(t *testing.T) {
	kinds := []error{app.ErrTransient, app.ErrRetryableConflict, app.ErrPermanent, domain.ErrWalletExists}
	tests := []struct {
		name      string
		err       error
		want      error
		retryable bool
	}{
		{"serialization failure", pgError("40001", ""), app.ErrTransient, true},
		{"deadlock", pgError("40P01", ""), app.ErrTransient, true},
		{"lock timeout", pgError("55P03", ""), app.ErrTransient, true},
		{"statement timeout", pgError("57014", ""), app.ErrTransient, false},
		{"admin shutdown", pgError("57P01", ""), app.ErrTransient, false},
		{"cannot connect now", pgError("57P03", ""), app.ErrTransient, false},
		{"connection failure", pgError("08006", ""), app.ErrTransient, false},
		{"too many connections", pgError("53300", ""), app.ErrTransient, false},
		{"out of memory", pgError("53200", ""), app.ErrTransient, false},
		{"idle in transaction timeout", pgError("25P03", ""), app.ErrTransient, false},
		{"idempotency key race", pgError("23505", "wager_transactions_idempotency_key"), app.ErrRetryableConflict, true},
		{"external id race", pgError("23505", "wager_transactions_external_id_key"), app.ErrRetryableConflict, true},
		{"reversal race", pgError("23505", "wager_transactions_one_reversal"), app.ErrRetryableConflict, true},
		{"wallet exists", pgError("23505", "wallets_player_currency_key"), domain.ErrWalletExists, false},
		{"duplicate ledger entry", pgError("23505", "ledger_entries_wallet_transaction_key"), app.ErrPermanent, false},
		{"duplicate event", pgError("23505", "outbox_events_pkey"), app.ErrPermanent, false},
		{"second opening", pgError("23505", "wager_transactions_one_opening"), app.ErrPermanent, false},
		{"negative balance", pgError("23514", "wallets_balance_non_negative"), app.ErrPermanent, false},
		{"ledger coupling", pgError("23000", "wallets_ledger_coupling"), app.ErrPermanent, false},
		{"terminal transaction", pgError("23000", "wager_transactions_terminal"), app.ErrPermanent, false},
		{"unknown provider", pgError("23503", "wager_transactions_provider_id_fkey"), app.ErrPermanent, false},
		{"write in a read-only transaction", pgError("25006", ""), app.ErrPermanent, false},
		{"insufficient privilege", pgError("42501", ""), app.ErrPermanent, false},
		{"context canceled", context.Canceled, app.ErrTransient, false},
		{"context deadline", fmt.Errorf("acquire: %w", context.DeadlineExceeded), app.ErrTransient, false},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, app.ErrTransient, false},
		{"connection dropped", io.ErrUnexpectedEOF, app.ErrTransient, false},
		{"commit after a failed statement", pgx.ErrTxCommitRollback, app.ErrPermanent, false},
		{"unexpected", errors.New("cannot scan"), app.ErrPermanent, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.err)
			if !errors.Is(got, tt.want) || !errors.Is(got, tt.err) {
				t.Fatalf("classify(%v) = %v; want %v wrapping the cause", tt.err, got, tt.want)
			}
			for _, other := range kinds {
				if other != tt.want && errors.Is(got, other) {
					t.Fatalf("classify(%v) = %v; also matches %v", tt.err, got, other)
				}
			}
			if retryable := retryReason(got) != ""; retryable != tt.retryable {
				t.Fatalf("retryable(%v) = %v; want %v", got, retryable, tt.retryable)
			}
		})
	}
}

func TestRetryReason(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{classify(pgError("40001", "")), "serialization"},
		{classify(pgError("40P01", "")), "deadlock"},
		{classify(pgError("55P03", "")), "lock_timeout"},
		{classify(pgError("23505", "wager_transactions_idempotency_key")), "conflict"},
		{app.ErrConcurrentUpdate, "conflict"},
		{classify(pgError("57014", "")), ""},
		{classify(pgError("23514", "wallets_balance_non_negative")), ""},
		{nil, ""},
	}
	for _, tt := range tests {
		if got := retryReason(tt.err); got != tt.want {
			t.Errorf("retryReason(%v) = %q; want %q", tt.err, got, tt.want)
		}
	}
}

func TestClassifyKeepsNil(t *testing.T) {
	if err := classify(nil); err != nil {
		t.Fatalf("classify(nil) = %v", err)
	}
}

func TestConcurrentUpdateIsRetryable(t *testing.T) {
	err := fmt.Errorf("%w: wallet is not at version 1", app.ErrConcurrentUpdate)
	if retryReason(err) != "conflict" || !errors.Is(err, app.ErrRetryableConflict) {
		t.Fatalf("retryReason(%v) = %q; want conflict", err, retryReason(err))
	}
}
