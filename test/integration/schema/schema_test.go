//go:build integration

package schema_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
)

const (
	integrityViolation    = "23000"
	foreignKeyViolation   = "23503"
	uniqueViolation       = "23505"
	checkViolation        = "23514"
	insufficientPrivilege = "42501"
)

var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T {
	return &v
}

func payloadHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireCode(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err = %v; want SQLSTATE %s %s", err, code, constraint)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("err = %s %q (%s); want %s %q", pgErr.Code, pgErr.ConstraintName, pgErr.Message, code, constraint)
	}
}

func inTx(t *testing.T, pool *pgxpool.Pool, fn func(*database.Queries) error) error {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(database.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type seeded struct {
	walletID  uuid.UUID
	playerID  uuid.UUID
	openingID uuid.UUID
}

func opening(s seeded, balance int64) database.InsertTransactionParams {
	return database.InsertTransactionParams{
		ID:                  s.openingID,
		Origin:              "INTERNAL",
		Kind:                "OPENING",
		Status:              "PROCESSED",
		WalletID:            s.walletID,
		PlayerID:            s.playerID,
		Currency:            "BRL",
		AmountMinor:         balance,
		CorrelationID:       "corr-open",
		ResultBalanceMinor:  ptr(balance),
		ResultWalletVersion: ptr(int64(1)),
		CreatedAt:           t0,
		UpdatedAt:           t0,
		CompletedAt:         ptr(t0),
	}
}

func journal(walletID, transactionID uuid.UUID, counterparty, playerDirection string, amount int64) []database.InsertLedgerPostingParams {
	other := "DEBIT"
	if playerDirection == "DEBIT" {
		other = "CREDIT"
	}
	return []database.InsertLedgerPostingParams{
		{WalletID: walletID, TransactionID: transactionID, Account: "PLAYER_BALANCES", Direction: playerDirection, AmountMinor: amount, Currency: "BRL", CreatedAt: t0},
		{WalletID: walletID, TransactionID: transactionID, Account: counterparty, Direction: other, AmountMinor: amount, Currency: "BRL", CreatedAt: t0},
	}
}

func post(ctx context.Context, q *database.Queries, postings ...database.InsertLedgerPostingParams) error {
	for _, p := range postings {
		if err := q.InsertLedgerPosting(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func betDebit(s seeded, transactionID uuid.UUID) database.InsertLedgerEntryParams {
	return database.InsertLedgerEntryParams{
		ID:                 ledger.EntryID(transactionID),
		WalletID:           s.walletID,
		TransactionID:      transactionID,
		Direction:          "DEBIT",
		AmountMinor:        2500,
		Currency:           "BRL",
		BalanceBeforeMinor: 10000,
		BalanceAfterMinor:  7500,
		WalletVersion:      2,
		CreatedAt:          t0,
	}
}

func betApplied(s seeded) database.UpdateWalletBalanceParams {
	return database.UpdateWalletBalanceParams{ID: s.walletID, Version: 1, BalanceMinor: 7500, UpdatedAt: t0}
}

func openWallet(t *testing.T, pool *pgxpool.Pool, balance int64) seeded {
	t.Helper()
	return seedWallet(t, pool, balance, true)
}

func seedWallet(t *testing.T, pool *pgxpool.Pool, balance int64, posted bool) seeded {
	t.Helper()
	s := seeded{walletID: uuid.New(), playerID: uuid.New()}
	s.openingID = wallet.OpeningTransactionID(s.walletID)
	err := inTx(t, pool, func(q *database.Queries) error {
		ctx := t.Context()
		if err := q.InsertWallet(ctx, database.InsertWalletParams{
			ID:           s.walletID,
			PlayerID:     s.playerID,
			Currency:     "BRL",
			BalanceMinor: balance,
			Version:      1,
			CreatedAt:    t0,
			UpdatedAt:    t0,
		}); err != nil {
			return err
		}
		if balance == 0 {
			return nil
		}
		if err := q.InsertTransaction(ctx, opening(s, balance)); err != nil {
			return err
		}
		if err := q.InsertLedgerEntry(ctx, database.InsertLedgerEntryParams{
			ID:                 ledger.EntryID(s.openingID),
			WalletID:           s.walletID,
			TransactionID:      s.openingID,
			Direction:          "CREDIT",
			AmountMinor:        balance,
			Currency:           "BRL",
			BalanceBeforeMinor: 0,
			BalanceAfterMinor:  balance,
			WalletVersion:      1,
			CreatedAt:          t0,
		}); err != nil || !posted {
			return err
		}
		return post(ctx, q, journal(s.walletID, s.openingID, "FUNDING", "CREDIT", balance)...)
	})
	if err != nil {
		t.Fatalf("open wallet: %v", err)
	}
	return s
}

func external(s seeded, extID, kind, status string, amount int64) database.InsertTransactionParams {
	p := database.InsertTransactionParams{
		ID:                    uuid.New(),
		Origin:                "EXTERNAL",
		Kind:                  kind,
		Status:                status,
		WalletID:              s.walletID,
		PlayerID:              s.playerID,
		Currency:              "BRL",
		AmountMinor:           amount,
		ProviderID:            ptr("provider-a"),
		ExternalTransactionID: ptr(extID),
		IdempotencyKey:        ptr("provider-a:" + extID),
		PayloadHash:           ptr(payloadHash(extID)),
		RoundID:               ptr("round-1"),
		GameID:                ptr("game-1"),
		CorrelationID:         "corr-" + extID,
		CreatedAt:             t0,
		UpdatedAt:             t0,
	}
	switch status {
	case "PROCESSED":
		p.ResultBalanceMinor, p.ResultWalletVersion, p.CompletedAt = ptr(int64(0)), ptr(int64(1)), ptr(t0)
	case "REJECTED":
		p.FailureCode, p.CompletedAt = ptr("INSUFFICIENT_FUNDS"), ptr(t0)
	case "PENDING_REFERENCE":
		p.Attempts, p.NextAttemptAt, p.ReferenceDeadlineAt = 1, ptr(t0), ptr(t0.Add(15*time.Minute))
	}
	return p
}

func reversal(s seeded, extID, kind, status string, ref database.InsertTransactionParams) database.InsertTransactionParams {
	p := external(s, extID, kind, status, ref.AmountMinor)
	p.ReferenceExternalTransactionID, p.ReferenceTransactionID = ref.ExternalTransactionID, ptr(ref.ID)
	return p
}

func requireAllApplied(t *testing.T, provider *goose.Provider, applied bool) {
	t.Helper()
	statuses, err := provider.Status(t.Context())
	if err != nil || len(statuses) == 0 {
		t.Fatalf("status = %v, %v", statuses, err)
	}
	for _, s := range statuses {
		if (s.State == goose.StateApplied) != applied {
			t.Fatalf("%s is %s; want applied=%v", s.Source.Path, s.State, applied)
		}
	}
}

func TestMigrationsUpResetUp(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	provider := db.Migrations(t)
	requireAllApplied(t, provider, true)
	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("reset: %v", err)
	}
	requireAllApplied(t, provider, false)
	var tables int
	must(t, db.App.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`).Scan(&tables))
	if tables != 0 {
		t.Fatalf("%d tables left after reset", tables)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up after reset: %v", err)
	}
	requireAllApplied(t, provider, true)
}

func TestWalletBalanceCannotGoNegative(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	err := database.New(db.App).InsertWallet(ctx, database.InsertWalletParams{
		ID:           uuid.New(),
		PlayerID:     uuid.New(),
		Currency:     "BRL",
		BalanceMinor: -1,
		Version:      1,
		CreatedAt:    t0,
		UpdatedAt:    t0,
	})
	requireCode(t, err, checkViolation, "wallets_balance_non_negative")

	s := openWallet(t, db.App, 10000)
	err = inTx(t, db.App, func(q *database.Queries) error {
		_, err := q.UpdateWalletBalance(ctx, database.UpdateWalletBalanceParams{ID: s.walletID, Version: 1, BalanceMinor: -100, UpdatedAt: t0})
		return err
	})
	requireCode(t, err, checkViolation, "wallets_balance_non_negative")
}

func TestLedgerIsAppendOnly(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	openWallet(t, db.App, 10000)
	owner := db.Migrator(t)
	for table, rows := range map[string]int{"ledger_entries": 1, "ledger_postings": 2} {
		statements := []string{
			`UPDATE ` + table + ` SET created_at = now()`,
			`DELETE FROM ` + table,
			`TRUNCATE ` + table,
		}
		for _, stmt := range statements {
			_, err := db.App.Exec(ctx, stmt)
			requireCode(t, err, insufficientPrivilege, "")
		}
		for _, stmt := range statements {
			_, err := owner.Exec(ctx, stmt)
			requireCode(t, err, integrityViolation, table+"_append_only")
		}
		var n int
		must(t, db.App.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n))
		if n != rows {
			t.Fatalf("%d rows in %s; want the opening's %d", n, table, rows)
		}
	}
}

func TestLedgerRejectsSecondEntryForTransaction(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	s := openWallet(t, db.App, 10000)
	err := inTx(t, db.App, func(q *database.Queries) error {
		return q.InsertLedgerEntry(t.Context(), database.InsertLedgerEntryParams{
			ID:                 uuid.New(),
			WalletID:           s.walletID,
			TransactionID:      s.openingID,
			Direction:          "CREDIT",
			AmountMinor:        10000,
			Currency:           "BRL",
			BalanceBeforeMinor: 10000,
			BalanceAfterMinor:  20000,
			WalletVersion:      2,
			CreatedAt:          t0,
		})
	})
	requireCode(t, err, uniqueViolation, "ledger_entries_wallet_transaction_key")
}

func TestSecondOpeningIsRejected(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	s := openWallet(t, db.App, 10000)
	second := opening(s, 5000)
	second.ID = uuid.New()
	err := database.New(db.App).InsertTransaction(t.Context(), second)
	requireCode(t, err, uniqueViolation, "wager_transactions_one_opening")
}

func TestSecondProcessedReversalIsRejected(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	q := database.New(db.App)
	s := openWallet(t, db.App, 10000)
	bet := external(s, "bet-1", "BET", "PROCESSED", 2500)
	must(t, q.InsertTransaction(ctx, bet))
	must(t, q.InsertTransaction(ctx, reversal(s, "refund-1", "REFUND", "PROCESSED", bet)))
	must(t, q.InsertTransaction(ctx, reversal(s, "rollback-1", "ROLLBACK", "REJECTED", bet)))
	err := q.InsertTransaction(ctx, reversal(s, "rollback-2", "ROLLBACK", "PROCESSED", bet))
	requireCode(t, err, uniqueViolation, "wager_transactions_one_reversal")
}

func TestTransactionUniqueness(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	q := database.New(db.App)
	s := openWallet(t, db.App, 10000)
	must(t, q.InsertTransaction(ctx, external(s, "bet-1", "BET", "PROCESSED", 2500)))

	sameKey := external(s, "bet-2", "BET", "PROCESSED", 2500)
	sameKey.IdempotencyKey = ptr("provider-a:bet-1")
	requireCode(t, q.InsertTransaction(ctx, sameKey), uniqueViolation, "wager_transactions_idempotency_key")

	sameExternalID := external(s, "bet-1", "BET", "PROCESSED", 2500)
	sameExternalID.IdempotencyKey = ptr("another-key")
	requireCode(t, q.InsertTransaction(ctx, sameExternalID), uniqueViolation, "wager_transactions_external_id_key")

	otherProvider := external(s, "bet-1", "BET", "PROCESSED", 2500)
	otherProvider.ProviderID = ptr("provider-b")
	must(t, q.InsertTransaction(ctx, otherProvider))
}

func TestTerminalTransactionIsFrozen(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	q := database.New(db.App)
	s := openWallet(t, db.App, 10000)
	bet := external(s, "bet-1", "BET", "PROCESSED", 2500)
	must(t, q.InsertTransaction(ctx, bet))

	_, err := db.App.Exec(ctx, `UPDATE wager_transactions
		SET status = 'REJECTED', failure_code = 'INSUFFICIENT_FUNDS', result_balance_minor = NULL, result_wallet_version = NULL
		WHERE id = $1`, bet.ID)
	requireCode(t, err, integrityViolation, "wager_transactions_terminal")
	_, err = db.App.Exec(ctx, `DELETE FROM wager_transactions WHERE id = $1`, bet.ID)
	requireCode(t, err, insufficientPrivilege, "")

	waiting := external(s, "refund-1", "REFUND", "PENDING_REFERENCE", 2500)
	waiting.ReferenceExternalTransactionID = ptr("bet-0")
	must(t, q.InsertTransaction(ctx, waiting))
	_, err = db.App.Exec(ctx, `UPDATE wager_transactions SET amount_minor = 3000 WHERE id = $1`, waiting.ID)
	requireCode(t, err, integrityViolation, "wager_transactions_immutable_fields")
	_, err = db.App.Exec(ctx, `UPDATE wager_transactions
		SET status = 'REJECTED', failure_code = 'REFERENCE_NOT_FOUND', updated_at = $2, completed_at = $2
		WHERE id = $1`, waiting.ID, t0.Add(time.Hour))
	must(t, err)
}

func TestBalanceChangeRequiresLedgerEntry(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	s := openWallet(t, db.App, 10000)

	err := inTx(t, db.App, func(q *database.Queries) error {
		_, err := q.UpdateWalletBalance(ctx, database.UpdateWalletBalanceParams{ID: s.walletID, Version: 1, BalanceMinor: 7500, UpdatedAt: t0})
		return err
	})
	requireCode(t, err, integrityViolation, "wallets_ledger_coupling")

	_, err = db.App.Exec(ctx, `UPDATE wallets SET version = version + 1 WHERE id = $1`, s.walletID)
	requireCode(t, err, integrityViolation, "wallets_ledger_coupling")

	err = database.New(db.App).InsertWallet(ctx, database.InsertWalletParams{
		ID:           uuid.New(),
		PlayerID:     uuid.New(),
		Currency:     "BRL",
		BalanceMinor: 100,
		Version:      1,
		CreatedAt:    t0,
		UpdatedAt:    t0,
	})
	requireCode(t, err, integrityViolation, "wallets_ledger_coupling")

	bet := external(s, "bet-1", "BET", "PROCESSED", 2500)
	err = inTx(t, db.App, func(q *database.Queries) error {
		if err := q.InsertTransaction(ctx, bet); err != nil {
			return err
		}
		if err := q.InsertLedgerEntry(ctx, betDebit(s, bet.ID)); err != nil {
			return err
		}
		return post(ctx, q, journal(s.walletID, bet.ID, "GAMING_REVENUE", "DEBIT", 2500)...)
	})
	requireCode(t, err, integrityViolation, "ledger_entries_wallet_coupling")

	err = inTx(t, db.App, func(q *database.Queries) error {
		if err := q.InsertTransaction(ctx, bet); err != nil {
			return err
		}
		if err := q.InsertLedgerEntry(ctx, betDebit(s, bet.ID)); err != nil {
			return err
		}
		if err := post(ctx, q, journal(s.walletID, bet.ID, "GAMING_REVENUE", "DEBIT", 2500)...); err != nil {
			return err
		}
		rows, err := q.UpdateWalletBalance(ctx, betApplied(s))
		if err == nil && rows != 1 {
			t.Errorf("UpdateWalletBalance touched %d rows", rows)
		}
		return err
	})
	must(t, err)
	var balance, version int64
	must(t, db.App.QueryRow(ctx, `SELECT balance_minor, version FROM wallets WHERE id = $1`, s.walletID).Scan(&balance, &version))
	if balance != 7500 || version != 2 {
		t.Fatalf("wallet = %d at version %d; want 7500 at version 2", balance, version)
	}
}

func TestJournalsBalanceAndMirrorTheWalletLedger(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	legs := func(w, tx uuid.UUID) []database.InsertLedgerPostingParams {
		return journal(w, tx, "GAMING_REVENUE", "DEBIT", 2500)
	}
	tests := []struct {
		name       string
		status     string
		moves      bool
		postings   func(w, tx uuid.UUID) []database.InsertLedgerPostingParams
		code       string
		constraint string
	}{
		{"a balanced journal", "PROCESSED", true, legs, "", ""},
		{"no journal", "PROCESSED", true, func(uuid.UUID, uuid.UUID) []database.InsertLedgerPostingParams {
			return nil
		}, integrityViolation, "ledger_journal_balanced"},
		{"a single leg", "PROCESSED", true, func(w, tx uuid.UUID) []database.InsertLedgerPostingParams {
			return legs(w, tx)[:1]
		}, integrityViolation, "ledger_journal_balanced"},
		{"both legs on the same side", "PROCESSED", true, func(w, tx uuid.UUID) []database.InsertLedgerPostingParams {
			p := legs(w, tx)
			p[1].Direction = "DEBIT"
			return p
		}, integrityViolation, "ledger_journal_balanced"},
		{"the opening's counterparty", "PROCESSED", true, func(w, tx uuid.UUID) []database.InsertLedgerPostingParams {
			return journal(w, tx, "FUNDING", "DEBIT", 2500)
		}, integrityViolation, "ledger_journal_accounts"},
		{"the player leg against the entry", "PROCESSED", true, func(w, tx uuid.UUID) []database.InsertLedgerPostingParams {
			return journal(w, tx, "GAMING_REVENUE", "CREDIT", 2500)
		}, integrityViolation, "ledger_journal_accounts"},
		{"a journal without a wallet entry", "PROCESSED", false, legs, integrityViolation, "ledger_journal_accounts"},
		{"a rejected transaction", "REJECTED", false, legs, integrityViolation, "ledger_journal_processed"},
		{"a leg for another amount", "PROCESSED", true, func(w, tx uuid.UUID) []database.InsertLedgerPostingParams {
			p := legs(w, tx)
			p[1].AmountMinor = 999
			return p
		}, foreignKeyViolation, "ledger_postings_transaction_fkey"},
		{"an account outside the chart", "PROCESSED", true, func(w, tx uuid.UUID) []database.InsertLedgerPostingParams {
			p := legs(w, tx)
			p[1].Account = "HOUSE"
			return p
		}, checkViolation, "ledger_postings_account"},
		{"two legs on one account", "PROCESSED", true, func(w, tx uuid.UUID) []database.InsertLedgerPostingParams {
			p := legs(w, tx)
			p[1].Account = "PLAYER_BALANCES"
			return p
		}, uniqueViolation, "ledger_postings_pkey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openWallet(t, db.App, 10000)
			bet := external(s, "bet-"+uuid.NewString(), "BET", tt.status, 2500)
			err := inTx(t, db.App, func(q *database.Queries) error {
				if err := q.InsertTransaction(ctx, bet); err != nil {
					return err
				}
				if tt.moves {
					if err := q.InsertLedgerEntry(ctx, betDebit(s, bet.ID)); err != nil {
						return err
					}
					if _, err := q.UpdateWalletBalance(ctx, betApplied(s)); err != nil {
						return err
					}
				}
				return post(ctx, q, tt.postings(s.walletID, bet.ID)...)
			})
			if tt.constraint == "" {
				must(t, err)
				return
			}
			requireCode(t, err, tt.code, tt.constraint)
		})
	}
}

func TestPostingsMigrationBackfillsTheLedger(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	provider := db.Migrations(t)
	if _, err := provider.DownTo(ctx, 11); err != nil {
		t.Fatalf("down to 11: %v", err)
	}
	s := seedWallet(t, db.App, 10000, false)
	bet := external(s, "bet-1", "BET", "PROCESSED", 2500)
	must(t, inTx(t, db.App, func(q *database.Queries) error {
		if err := q.InsertTransaction(ctx, bet); err != nil {
			return err
		}
		if err := q.InsertLedgerEntry(ctx, betDebit(s, bet.ID)); err != nil {
			return err
		}
		_, err := q.UpdateWalletBalance(ctx, betApplied(s))
		return err
	}))
	if _, err := provider.UpTo(ctx, 12); err != nil {
		t.Fatalf("up to 12: %v", err)
	}

	rows, err := db.App.Query(ctx, `SELECT transaction_id, account, direction, amount_minor FROM ledger_postings`)
	must(t, err)
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var (
			txID               uuid.UUID
			account, direction string
			amount             int64
		)
		must(t, rows.Scan(&txID, &account, &direction, &amount))
		got[txID.String()+" "+account] = fmt.Sprintf("%s %d", direction, amount)
	}
	must(t, rows.Err())
	want := map[string]string{
		s.openingID.String() + " FUNDING":         "DEBIT 10000",
		s.openingID.String() + " PLAYER_BALANCES": "CREDIT 10000",
		bet.ID.String() + " PLAYER_BALANCES":      "DEBIT 2500",
		bet.ID.String() + " GAMING_REVENUE":       "CREDIT 2500",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backfilled postings = %v; want %v", got, want)
	}

	win := external(s, "win-1", "WIN", "PROCESSED", 1000)
	err = inTx(t, db.App, func(q *database.Queries) error {
		if err := q.InsertTransaction(ctx, win); err != nil {
			return err
		}
		if err := q.InsertLedgerEntry(ctx, database.InsertLedgerEntryParams{
			ID: ledger.EntryID(win.ID), WalletID: s.walletID, TransactionID: win.ID, Direction: "CREDIT", AmountMinor: 1000,
			Currency: "BRL", BalanceBeforeMinor: 7500, BalanceAfterMinor: 8500, WalletVersion: 3, CreatedAt: t0,
		}); err != nil {
			return err
		}
		_, err := q.UpdateWalletBalance(ctx, database.UpdateWalletBalanceParams{ID: s.walletID, Version: 2, BalanceMinor: 8500, UpdatedAt: t0})
		return err
	})
	requireCode(t, err, integrityViolation, "ledger_journal_balanced")
}

func TestWalletIdentityIsImmutable(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	s := openWallet(t, db.App, 0)
	_, err := db.App.Exec(ctx, `UPDATE wallets SET currency = 'USD' WHERE id = $1`, s.walletID)
	requireCode(t, err, integrityViolation, "wallets_immutable_fields")
	_, err = db.App.Exec(ctx, `DELETE FROM wallets WHERE id = $1`, s.walletID)
	requireCode(t, err, insufficientPrivilege, "")
}

func TestOutboxEnvelopeIsImmutable(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	id, walletID := uuid.New(), uuid.New()
	must(t, database.New(db.App).InsertOutboxEvent(ctx, database.InsertOutboxEventParams{
		ID:            id,
		AggregateType: "Wallet",
		AggregateID:   walletID,
		PartitionKey:  walletID,
		EventType:     "WalletBalanceChanged",
		EventVersion:  1,
		CorrelationID: "corr-1",
		Payload:       []byte(`{"eventType":"WalletBalanceChanged"}`),
		OccurredAt:    t0,
		NextAttemptAt: t0,
	}))
	for _, stmt := range []string{
		`UPDATE outbox_events SET payload = '{}' WHERE id = $1`,
		`UPDATE outbox_events SET event_type = 'WagerTransactionProcessed' WHERE id = $1`,
		`UPDATE outbox_events SET id = gen_random_uuid() WHERE id = $1`,
	} {
		_, err := db.App.Exec(ctx, stmt, id)
		requireCode(t, err, integrityViolation, "outbox_events_immutable")
	}
	_, err := db.App.Exec(ctx, `UPDATE outbox_events SET attempts = attempts + 1, published_at = now() WHERE id = $1`, id)
	must(t, err)
	_, err = db.App.Exec(ctx, `DELETE FROM outbox_events WHERE id = $1`, id)
	requireCode(t, err, insufficientPrivilege, "")
}

func TestAppRoleCannotChangeSchema(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	for _, stmt := range []string{
		`CREATE TABLE scratch (id int)`,
		`ALTER TABLE ledger_entries DISABLE TRIGGER ALL`,
		`DROP TABLE wallets`,
	} {
		_, err := db.App.Exec(t.Context(), stmt)
		requireCode(t, err, insufficientPrivilege, "")
	}
}

func TestSchemaRejectsInvalidRows(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	ctx := t.Context()
	q := database.New(db.App)
	insert := func(p database.InsertTransactionParams) error { return q.InsertTransaction(ctx, p) }
	tests := []struct {
		name       string
		run        func(s seeded) error
		code       string
		constraint string
	}{
		{"loss with an amount", func(s seeded) error {
			return insert(external(s, "loss-1", "LOSS", "PROCESSED", 100))
		}, checkViolation, "wager_transactions_zero_policy"},
		{"zero bet", func(s seeded) error {
			return insert(external(s, "bet-1", "BET", "PROCESSED", 0))
		}, checkViolation, "wager_transactions_zero_policy"},
		{"refund without a reference", func(s seeded) error {
			return insert(external(s, "refund-1", "REFUND", "PROCESSED", 100))
		}, checkViolation, "wager_transactions_reference_required"},
		{"bet with a reference", func(s seeded) error {
			p := external(s, "bet-1", "BET", "PROCESSED", 100)
			p.ReferenceExternalTransactionID = ptr("bet-0")
			return insert(p)
		}, checkViolation, "wager_transactions_reference_allowed"},
		{"reference to itself", func(s seeded) error {
			p := external(s, "win-1", "WIN", "PROCESSED", 100)
			p.ReferenceExternalTransactionID = ptr("win-1")
			return insert(p)
		}, checkViolation, "wager_transactions_reference_format"},
		{"pending is never stored", func(s seeded) error {
			return insert(external(s, "bet-1", "BET", "PENDING", 100))
		}, checkViolation, "wager_transactions_status"},
		{"opening with external metadata", func(s seeded) error {
			p := opening(s, 100)
			p.ID, p.ProviderID = uuid.New(), ptr("provider-a")
			return insert(p)
		}, checkViolation, "wager_transactions_internal_fields"},
		{"external without a payload hash", func(s seeded) error {
			p := external(s, "bet-1", "BET", "PROCESSED", 100)
			p.PayloadHash = nil
			return insert(p)
		}, checkViolation, "wager_transactions_external_fields"},
		{"idempotency key with a space", func(s seeded) error {
			p := external(s, "bet-1", "BET", "PROCESSED", 100)
			p.IdempotencyKey = ptr("provider-a: bet-1")
			return insert(p)
		}, checkViolation, "wager_transactions_external_fields"},
		{"rejected without a failure code", func(s seeded) error {
			p := external(s, "bet-1", "BET", "REJECTED", 100)
			p.FailureCode = nil
			return insert(p)
		}, checkViolation, "wager_transactions_failure_code"},
		{"processed without a result", func(s seeded) error {
			p := external(s, "bet-1", "BET", "PROCESSED", 100)
			p.ResultBalanceMinor, p.ResultWalletVersion = nil, nil
			return insert(p)
		}, checkViolation, "wager_transactions_result"},
		{"pending reference without a schedule", func(s seeded) error {
			p := external(s, "refund-1", "REFUND", "PENDING_REFERENCE", 100)
			p.ReferenceExternalTransactionID, p.NextAttemptAt = ptr("bet-0"), nil
			return insert(p)
		}, checkViolation, "wager_transactions_pending_reference"},
		{"terminal without completion time", func(s seeded) error {
			p := external(s, "bet-1", "BET", "PROCESSED", 100)
			p.CompletedAt = nil
			return insert(p)
		}, checkViolation, "wager_transactions_completed_at"},
		{"unknown provider", func(s seeded) error {
			p := external(s, "bet-1", "BET", "PROCESSED", 100)
			p.ProviderID = ptr("provider-z")
			return insert(p)
		}, foreignKeyViolation, "wager_transactions_provider_id_fkey"},
		{"ledger arithmetic", func(s seeded) error {
			bet := external(s, "arithmetic-bet", "BET", "PROCESSED", 2500)
			if err := insert(bet); err != nil {
				return err
			}
			return q.InsertLedgerEntry(ctx, database.InsertLedgerEntryParams{
				ID: uuid.New(), WalletID: s.walletID, TransactionID: bet.ID, Direction: "DEBIT", AmountMinor: 2500,
				Currency: "BRL", BalanceBeforeMinor: 10000, BalanceAfterMinor: 10000, WalletVersion: 2, CreatedAt: t0,
			})
		}, checkViolation, "ledger_entries_arithmetic"},
		{"ledger amount differs from its transaction", func(s seeded) error {
			bet := external(s, "mismatched-bet", "BET", "PROCESSED", 2500)
			if err := insert(bet); err != nil {
				return err
			}
			return q.InsertLedgerEntry(ctx, database.InsertLedgerEntryParams{
				ID: uuid.New(), WalletID: s.walletID, TransactionID: bet.ID, Direction: "DEBIT", AmountMinor: 999,
				Currency: "BRL", BalanceBeforeMinor: 10000, BalanceAfterMinor: 9001, WalletVersion: 2, CreatedAt: t0,
			})
		}, foreignKeyViolation, "ledger_entries_transaction_fkey"},
		{"ledger entry for a loss", func(s seeded) error {
			loss := external(s, "ledger-loss", "LOSS", "PROCESSED", 0)
			if err := insert(loss); err != nil {
				return err
			}
			return q.InsertLedgerEntry(ctx, database.InsertLedgerEntryParams{
				ID: uuid.New(), WalletID: s.walletID, TransactionID: loss.ID, Direction: "DEBIT", AmountMinor: 0,
				Currency: "BRL", BalanceBeforeMinor: 10000, BalanceAfterMinor: 10000, WalletVersion: 2, CreatedAt: t0,
			})
		}, checkViolation, "ledger_entries_amount_positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireCode(t, tt.run(openWallet(t, db.App, 10000)), tt.code, tt.constraint)
		})
	}
}
