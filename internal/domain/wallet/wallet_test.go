package wallet_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

var (
	t0       = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	t1       = t0.Add(time.Minute)
	walletID = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
	playerID = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	betID    = uuid.MustParse("0192f298-345e-7e38-af88-e43f851a819d")
	winID    = uuid.MustParse("0192f299-1f0a-7c11-9d2e-3a4b5c6d7e8f")
)

func brl(t *testing.T, minor int64) money.Money {
	t.Helper()
	m, err := money.FromMinor(minor, money.BRL)
	if err != nil {
		t.Fatalf("FromMinor(%d): %v", minor, err)
	}
	return m
}

func stored(t *testing.T, minor, version int64) *wallet.Wallet {
	t.Helper()
	w, err := wallet.Rehydrate(wallet.Snapshot{
		ID:        walletID,
		PlayerID:  playerID,
		Balance:   brl(t, minor),
		Version:   version,
		CreatedAt: t0,
		UpdatedAt: t0,
	})
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	return w
}

func TestNewWithInitialBalance(t *testing.T) {
	w, entry, err := wallet.New(walletID, playerID, brl(t, 100000), t0)
	if err != nil {
		t.Fatal(err)
	}
	want := wallet.Snapshot{ID: walletID, PlayerID: playerID, Balance: brl(t, 100000), Version: 1, CreatedAt: t0, UpdatedAt: t0}
	if got := w.Snapshot(); got != want {
		t.Fatalf("wallet = %+v; want %+v", got, want)
	}
	openingID := wallet.OpeningTransactionID(walletID)
	wantEntry := ledger.Snapshot{
		ID:            ledger.EntryID(openingID),
		WalletID:      walletID,
		TransactionID: openingID,
		Direction:     ledger.Credit,
		Amount:        brl(t, 100000),
		BalanceBefore: brl(t, 0),
		BalanceAfter:  brl(t, 100000),
		WalletVersion: 1,
		CreatedAt:     t0,
	}
	if entry == nil || entry.Snapshot() != wantEntry {
		t.Fatalf("opening entry = %+v; want %+v", entry, wantEntry)
	}
}

func TestNewWithZeroBalance(t *testing.T) {
	w, entry, err := wallet.New(walletID, playerID, brl(t, 0), t0)
	if err != nil || entry != nil {
		t.Fatalf("New = entry %+v, %v; want no entry", entry, err)
	}
	want := wallet.Snapshot{ID: walletID, PlayerID: playerID, Balance: brl(t, 0), Version: 1, CreatedAt: t0, UpdatedAt: t0}
	if got := w.Snapshot(); got != want {
		t.Fatalf("wallet = %+v; want %+v", got, want)
	}
}

func TestNewRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		id       uuid.UUID
		playerID uuid.UUID
		initial  money.Money
		now      time.Time
		want     string
	}{
		{"missing id", uuid.Nil, playerID, brl(t, 0), t0, "id: REQUIRED"},
		{"missing player", walletID, uuid.Nil, brl(t, 0), t0, "playerId: REQUIRED"},
		{"negative initial", walletID, playerID, brl(t, -1), t0, "initialBalance: INVALID_AMOUNT"},
		{"uninitialized initial", walletID, playerID, money.Money{}, t0, "initialBalance: REQUIRED"},
		{"missing time", walletID, playerID, brl(t, 0), time.Time{}, "createdAt: REQUIRED"},
		{"everything", uuid.Nil, uuid.Nil, money.Money{}, time.Time{},
			"id: REQUIRED\nplayerId: REQUIRED\ninitialBalance: REQUIRED\ncreatedAt: REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, entry, err := wallet.New(tt.id, tt.playerID, tt.initial, tt.now)
			if err == nil || err.Error() != tt.want || w != nil || entry != nil {
				t.Fatalf("New = %v, %v, %v; want error %q", w, entry, err, tt.want)
			}
		})
	}
}

func TestOpeningTransactionID(t *testing.T) {
	id := wallet.OpeningTransactionID(walletID)
	if id != wallet.OpeningTransactionID(walletID) || id.Version() != 5 {
		t.Fatalf("OpeningTransactionID = %s; want a stable version 5 UUID", id)
	}
	if id == wallet.OpeningTransactionID(playerID) {
		t.Fatalf("two wallets share the opening id %s", id)
	}
}

func TestRehydrate(t *testing.T) {
	s := wallet.Snapshot{ID: walletID, PlayerID: playerID, Balance: brl(t, 2000), Version: 7, CreatedAt: t0, UpdatedAt: t1}
	w, err := wallet.Rehydrate(s)
	if err != nil || w.Snapshot() != s {
		t.Fatalf("Rehydrate = %+v, %v; want %+v", w, err, s)
	}
	if w.ID() != walletID || w.PlayerID() != playerID || w.Balance() != brl(t, 2000) || w.Currency() != money.BRL || w.Version() != 7 {
		t.Fatalf("accessors disagree with the snapshot %+v", s)
	}
}

func TestRehydrateRejectsInvalidSnapshots(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*wallet.Snapshot)
		want   string
	}{
		{"missing id", func(s *wallet.Snapshot) { s.ID = uuid.Nil }, "id: REQUIRED"},
		{"missing player", func(s *wallet.Snapshot) { s.PlayerID = uuid.Nil }, "playerId: REQUIRED"},
		{"negative balance", func(s *wallet.Snapshot) { s.Balance = brl(t, -1) }, "balance: INVALID_AMOUNT"},
		{"uninitialized balance", func(s *wallet.Snapshot) { s.Balance = money.Money{} }, "balance: REQUIRED"},
		{"version zero", func(s *wallet.Snapshot) { s.Version = 0 }, "version: INVALID_VALUE"},
		{"missing created at", func(s *wallet.Snapshot) { s.CreatedAt = time.Time{} }, "createdAt: REQUIRED"},
		{"missing updated at", func(s *wallet.Snapshot) { s.UpdatedAt = time.Time{} }, "updatedAt: REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stored(t, 100, 1).Snapshot()
			tt.mutate(&s)
			w, err := wallet.Rehydrate(s)
			if err == nil || err.Error() != tt.want || w != nil {
				t.Fatalf("Rehydrate = %v, %v; want error %q", w, err, tt.want)
			}
		})
	}
}

func TestDebitAndCredit(t *testing.T) {
	w := stored(t, 10000, 3)
	entry, err := w.Debit(betID, brl(t, 2500), t1)
	if err != nil {
		t.Fatal(err)
	}
	want := ledger.Snapshot{
		ID:            ledger.EntryID(betID),
		WalletID:      walletID,
		TransactionID: betID,
		Direction:     ledger.Debit,
		Amount:        brl(t, 2500),
		BalanceBefore: brl(t, 10000),
		BalanceAfter:  brl(t, 7500),
		WalletVersion: 4,
		CreatedAt:     t1,
	}
	if entry.Snapshot() != want {
		t.Fatalf("debit entry = %+v; want %+v", entry.Snapshot(), want)
	}
	entry, err = w.Credit(winID, brl(t, 1000), t1)
	if err != nil {
		t.Fatal(err)
	}
	want = ledger.Snapshot{
		ID:            ledger.EntryID(winID),
		WalletID:      walletID,
		TransactionID: winID,
		Direction:     ledger.Credit,
		Amount:        brl(t, 1000),
		BalanceBefore: brl(t, 7500),
		BalanceAfter:  brl(t, 8500),
		WalletVersion: 5,
		CreatedAt:     t1,
	}
	if entry.Snapshot() != want {
		t.Fatalf("credit entry = %+v; want %+v", entry.Snapshot(), want)
	}
	wantWallet := wallet.Snapshot{ID: walletID, PlayerID: playerID, Balance: brl(t, 8500), Version: 5, CreatedAt: t0, UpdatedAt: t1}
	if got := w.Snapshot(); got != wantWallet {
		t.Fatalf("wallet = %+v; want %+v", got, wantWallet)
	}
}

func TestDebitWholeBalance(t *testing.T) {
	w := stored(t, 10000, 1)
	if _, err := w.Debit(betID, brl(t, 10000), t1); err != nil {
		t.Fatal(err)
	}
	if w.Balance() != brl(t, 0) || w.Version() != 2 {
		t.Fatalf("wallet = %+v; want 0.00 at version 2", w.Snapshot())
	}
}

func TestRejectedMovesLeaveWalletUnchanged(t *testing.T) {
	usd, err := money.FromMinor(100, money.USD)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		balance int64
		move    func(*wallet.Wallet) (ledger.Entry, error)
		want    error
		message string
	}{
		{"insufficient funds", 10000, func(w *wallet.Wallet) (ledger.Entry, error) {
			return w.Debit(betID, brl(t, 10001), t1)
		}, domain.ErrInsufficientFunds, ""},
		{"credit overflow", math.MaxInt64, func(w *wallet.Wallet) (ledger.Entry, error) {
			return w.Credit(winID, brl(t, 1), t1)
		}, domain.ErrBalanceOverflow, ""},
		{"debit in another currency", 10000, func(w *wallet.Wallet) (ledger.Entry, error) {
			return w.Debit(betID, usd, t1)
		}, domain.ErrCurrencyMismatch, ""},
		{"credit in another currency", 10000, func(w *wallet.Wallet) (ledger.Entry, error) {
			return w.Credit(winID, usd, t1)
		}, domain.ErrCurrencyMismatch, ""},
		{"zero debit", 10000, func(w *wallet.Wallet) (ledger.Entry, error) {
			return w.Debit(betID, brl(t, 0), t1)
		}, domain.ErrInvalidAmount, "amount: INVALID_AMOUNT"},
		{"negative credit", 10000, func(w *wallet.Wallet) (ledger.Entry, error) {
			return w.Credit(winID, brl(t, -100), t1)
		}, domain.ErrInvalidAmount, "amount: INVALID_AMOUNT"},
		{"uninitialized amount", 10000, func(w *wallet.Wallet) (ledger.Entry, error) {
			return w.Debit(betID, money.Money{}, t1)
		}, domain.ErrRequired, "amount: REQUIRED"},
		{"missing transaction", 10000, func(w *wallet.Wallet) (ledger.Entry, error) {
			return w.Credit(uuid.Nil, brl(t, 100), t1)
		}, domain.ErrRequired, "transactionId: REQUIRED"},
		{"missing time", 10000, func(w *wallet.Wallet) (ledger.Entry, error) {
			return w.Debit(betID, brl(t, 100), time.Time{})
		}, domain.ErrRequired, "createdAt: REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := stored(t, tt.balance, 1)
			before := w.Snapshot()
			entry, err := tt.move(w)
			if !errors.Is(err, tt.want) || entry != (ledger.Entry{}) {
				t.Fatalf("move = %+v, %v; want %v", entry.Snapshot(), err, tt.want)
			}
			if tt.message != "" && err.Error() != tt.message {
				t.Fatalf("error = %q; want %q", err, tt.message)
			}
			if after := w.Snapshot(); after != before {
				t.Fatalf("wallet changed from %+v to %+v", before, after)
			}
		})
	}
}

func TestVersionChangesOnlyWithBalance(t *testing.T) {
	w := stored(t, 5000, 1)
	steps := []struct {
		move    func() error
		version int64
		balance int64
	}{
		{func() error { _, err := w.Debit(betID, brl(t, 5001), t1); return err }, 1, 5000},
		{func() error { _, err := w.Credit(winID, brl(t, 0), t1); return err }, 1, 5000},
		{func() error { _, err := w.Credit(winID, brl(t, 1000), t1); return err }, 2, 6000},
		{func() error { _, err := w.Debit(betID, brl(t, 6000), t1); return err }, 3, 0},
		{func() error { _, err := w.Debit(betID, brl(t, 1), t1); return err }, 3, 0},
	}
	for i, step := range steps {
		_ = step.move()
		if w.Version() != step.version || w.Balance() != brl(t, step.balance) {
			t.Fatalf("after step %d: version %d balance %s; want %d and %s", i, w.Version(), w.Balance(), step.version, brl(t, step.balance))
		}
	}
}

func TestVersionOverflowIsRejected(t *testing.T) {
	w := stored(t, 100, math.MaxInt64)
	before := w.Snapshot()
	if _, err := w.Credit(winID, brl(t, 100), t1); err == nil || err.Error() != "walletVersion: INVALID_VALUE" {
		t.Fatalf("Credit at max version: err = %v", err)
	}
	if w.Snapshot() != before {
		t.Fatalf("wallet changed to %+v", w.Snapshot())
	}
}

func TestUninitializedWallet(t *testing.T) {
	var nilWallet *wallet.Wallet
	if _, err := nilWallet.Debit(betID, brl(t, 100), t1); !errors.Is(err, domain.ErrUninitialized) {
		t.Errorf("Debit on nil wallet: err = %v", err)
	}
	if _, err := (&wallet.Wallet{}).Credit(winID, brl(t, 100), t1); !errors.Is(err, domain.ErrUninitialized) {
		t.Errorf("Credit on zero wallet: err = %v", err)
	}
}
