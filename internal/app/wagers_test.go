package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

type memWallets struct {
	app.Wallets
	byID map[uuid.UUID]*wallet.Wallet
}

func (m *memWallets) GetForUpdate(_ context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	if w, ok := m.byID[id]; ok {
		return w, nil
	}
	return nil, domain.ErrWalletNotFound
}

func (m *memWallets) Update(context.Context, *wallet.Wallet, int64) error {
	return nil
}

type memTransactions struct {
	app.Transactions
	stored []*wager.Transaction
}

func (m *memTransactions) find(match func(wager.Snapshot) bool) (*wager.Transaction, error) {
	for _, tx := range m.stored {
		if match(tx.Snapshot()) {
			return tx, nil
		}
	}
	return nil, domain.ErrTransactionNotFound
}

func (m *memTransactions) GetByIdempotencyKey(_ context.Context, providerID, key string) (*wager.Transaction, error) {
	return m.find(func(s wager.Snapshot) bool { return s.ProviderID == providerID && s.IdempotencyKey == key })
}

func (m *memTransactions) GetByExternalID(_ context.Context, providerID, externalID string) (*wager.Transaction, error) {
	return m.find(func(s wager.Snapshot) bool {
		return s.ProviderID == providerID && s.ExternalTransactionID == externalID
	})
}

func (m *memTransactions) Insert(_ context.Context, tx *wager.Transaction) error {
	m.stored = append(m.stored, tx)
	return nil
}

func (m *memTransactions) WakeDependents(context.Context, string, string, uuid.UUID, time.Time) (int64, error) {
	return 0, nil
}

type nopLedger struct {
	app.Ledger
}

func (nopLedger) Insert(context.Context, ledger.Entry) error {
	return nil
}

type nopOutbox struct {
	app.Outbox
}

func (nopOutbox) Insert(context.Context, ...events.Event) error {
	return nil
}

type memStore struct {
	app.Store
	wallets *memWallets
	txs     *memTransactions
}

func (s memStore) Wallets() app.Wallets           { return s.wallets }
func (s memStore) Transactions() app.Transactions { return s.txs }
func (s memStore) Ledger() app.Ledger             { return nopLedger{} }
func (s memStore) Outbox() app.Outbox             { return nopOutbox{} }

type memRunner struct {
	st memStore
}

func (r memRunner) InTx(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return fn(ctx, r.st)
}

func (r memRunner) InReadOnlySnapshot(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return fn(ctx, r.st)
}

func TestSubmitDecidesReplayAndConflicts(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	initial, err := money.Parse("100.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := wallet.New(uuid.MustParse(walletID), uuid.MustParse(playerID), initial, now)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := wager.NewRules(time.Minute, 20, func(int) time.Duration { return time.Second })
	if err != nil {
		t.Fatal(err)
	}
	txs := &memTransactions{}
	store := memStore{wallets: &memWallets{byID: map[uuid.UUID]*wallet.Wallet{w.ID(): w}}, txs: txs}
	svc := app.NewWagerService(memRunner{store}, rules, func() time.Time { return now }, app.NewID)

	original, err := svc.Submit(t.Context(), command(t, bet()))
	if err != nil || original.IdempotentReplay || original.Transaction.Status != wager.Processed {
		t.Fatalf("first submission = %+v, %v; want a fresh PROCESSED bet", original, err)
	}

	moreMoney := bet()
	moreMoney.Money.Amount = "30.00"
	otherRound := bet()
	otherRound.RoundID = "round-988"
	tests := []struct {
		name    string
		request app.WagerRequest
		key     string
		want    error
	}{
		{"same key and payload replays", bet(), "provider-a:transaction-123", nil},
		{"same key, another amount", moreMoney, "provider-a:transaction-123", app.ErrIdempotencyKeyReused},
		{"same key, another round", otherRound, "provider-a:transaction-123", app.ErrIdempotencyKeyReused},
		{"same external id under another key", bet(), "provider-a:transaction-123:retry", app.ErrExternalIDConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := tt.request.Command(tt.key, "corr-2")
			if err != nil {
				t.Fatal(err)
			}
			got, err := svc.Submit(t.Context(), cmd)
			switch {
			case tt.want != nil && !errors.Is(err, tt.want):
				t.Fatalf("err = %v; want %v", err, tt.want)
			case tt.want == nil && (err != nil || !got.IdempotentReplay || got.Transaction.ID != original.Transaction.ID ||
				got.Transaction.Result.Balance != original.Transaction.Result.Balance):
				t.Fatalf("replay = %+v, %v; want the stored result of %s", got, err, original.Transaction.ID)
			case len(txs.stored) != 1:
				t.Fatalf("%d transactions stored; want only the original", len(txs.stored))
			}
		})
	}

	other := bet()
	other.ProviderID = "provider-b"
	cmd, err := other.Command("provider-a:transaction-123", "corr-3")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.Submit(t.Context(), cmd)
	if err != nil || fresh.IdempotentReplay || fresh.Transaction.ID == original.Transaction.ID || len(txs.stored) != 2 {
		t.Fatalf("same key and external id from provider-b = %+v, %v; want a separate operation", fresh, err)
	}
}
