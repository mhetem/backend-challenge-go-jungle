//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func config(url string) postgres.Config {
	return postgres.Config{
		URL:              url,
		ApplicationName:  "wallet-test",
		MaxConns:         8,
		LockTimeout:      2 * time.Second,
		StatementTimeout: 5 * time.Second,
		TxAttempts:       3,
		TxBackoff:        10 * time.Millisecond,
	}
}

func newRunner(t *testing.T, cfg postgres.Config) *postgres.TxRunner {
	t.Helper()
	pool, err := postgres.NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return postgres.NewTxRunner(pool, cfg)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func brl(t *testing.T, minor int64) money.Money {
	t.Helper()
	m, err := money.FromMinor(minor, money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newRules(t *testing.T) wager.Rules {
	t.Helper()
	rules, err := wager.NewRules(15*time.Minute, 20, func(int) time.Duration { return time.Second })
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func openWallet(t *testing.T, r *postgres.TxRunner, minor int64) wager.Opened {
	t.Helper()
	opened, err := wager.Open(uuid.New(), uuid.New(), brl(t, minor), "corr-open", t0)
	must(t, err)
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		if err := s.Wallets().Insert(ctx, opened.Wallet); err != nil {
			return err
		}
		if opened.Transaction == nil {
			return nil
		}
		if err := s.Transactions().Insert(ctx, opened.Transaction); err != nil {
			return err
		}
		if err := s.Ledger().Insert(ctx, *opened.Entry); err != nil {
			return err
		}
		return s.Outbox().Insert(ctx, opened.Events...)
	}))
	return opened
}

func payload(t *testing.T, w *wallet.Wallet, kind wager.Kind, extID string, minor int64) wager.Payload {
	t.Helper()
	return wager.Payload{
		ProviderID:            "provider-a",
		ExternalTransactionID: extID,
		PlayerID:              w.PlayerID(),
		WalletID:              w.ID(),
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  kind,
		Money:                 brl(t, minor),
	}
}

func external(t *testing.T, p wager.Payload, now time.Time) *wager.Transaction {
	t.Helper()
	tx, err := wager.NewExternal(wager.ExternalParams{
		Payload:        p,
		ID:             uuid.New(),
		IdempotencyKey: p.ProviderID + ":" + p.ExternalTransactionID,
		PayloadHash:    app.PayloadHash(p),
		CorrelationID:  "corr-" + p.ExternalTransactionID,
	}, now)
	must(t, err)
	return tx
}

func submit(t *testing.T, r *postgres.TxRunner, p wager.Payload, now time.Time) (*wager.Transaction, wager.Outcome) {
	t.Helper()
	var tx *wager.Transaction
	var out wager.Outcome
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		w, err := s.Wallets().GetForUpdate(ctx, p.WalletID)
		if err != nil {
			return err
		}
		version := w.Version()
		tx = external(t, p, now)
		if out, err = newRules(t).Apply(tx, w, nil, now); err != nil {
			return err
		}
		if err := s.Transactions().Insert(ctx, tx); err != nil {
			return err
		}
		return persist(ctx, s, w, version, out)
	}))
	return tx, out
}

func persist(ctx context.Context, s app.Store, w *wallet.Wallet, version int64, out wager.Outcome) error {
	if out.Entry != nil {
		if err := s.Ledger().Insert(ctx, *out.Entry); err != nil {
			return err
		}
		if err := s.Wallets().Update(ctx, w, version); err != nil {
			return err
		}
	}
	return s.Outbox().Insert(ctx, out.Events...)
}

func getWallet(t *testing.T, r *postgres.TxRunner, id uuid.UUID) *wallet.Wallet {
	t.Helper()
	var w *wallet.Wallet
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		var err error
		w, err = s.Wallets().Get(ctx, id)
		return err
	}))
	return w
}

func getTransaction(t *testing.T, r *postgres.TxRunner, id uuid.UUID) *wager.Transaction {
	t.Helper()
	var tx *wager.Transaction
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		var err error
		tx, err = s.Transactions().Get(ctx, id)
		return err
	}))
	return tx
}

func rehydrate(t *testing.T, s wager.Snapshot) *wager.Transaction {
	t.Helper()
	tx, err := wager.Rehydrate(s)
	if err != nil {
		t.Fatalf("Rehydrate(%s): %v", s.ExternalTransactionID, err)
	}
	return tx
}
