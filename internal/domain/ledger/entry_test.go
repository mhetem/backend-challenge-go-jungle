package ledger_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

var (
	t0            = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	walletID      = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
	transactionID = uuid.MustParse("0192f298-345e-7e38-af88-e43f851a819d")
)

func amount(t *testing.T, minor int64, cur money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(minor, cur)
	if err != nil {
		t.Fatalf("FromMinor(%d, %s): %v", minor, cur, err)
	}
	return m
}

func debit(t *testing.T) ledger.Snapshot {
	return ledger.Snapshot{
		ID:            ledger.EntryID(transactionID),
		WalletID:      walletID,
		TransactionID: transactionID,
		Direction:     ledger.Debit,
		Amount:        amount(t, 2500, money.BRL),
		BalanceBefore: amount(t, 10000, money.BRL),
		BalanceAfter:  amount(t, 7500, money.BRL),
		WalletVersion: 2,
		CreatedAt:     t0,
	}
}

func TestNew(t *testing.T) {
	credit := debit(t)
	credit.Direction = ledger.Credit
	credit.BalanceAfter = amount(t, 12500, money.BRL)
	drain := debit(t)
	drain.Amount = amount(t, 10000, money.BRL)
	drain.BalanceAfter = amount(t, 0, money.BRL)
	opening := debit(t)
	opening.Direction = ledger.Credit
	opening.BalanceBefore = amount(t, 0, money.BRL)
	opening.BalanceAfter = amount(t, 2500, money.BRL)
	opening.WalletVersion = 1
	for name, s := range map[string]ledger.Snapshot{"debit": debit(t), "credit": credit, "drain": drain, "opening": opening} {
		t.Run(name, func(t *testing.T) {
			e, err := ledger.New(s)
			if err != nil || e.Snapshot() != s {
				t.Fatalf("New = %+v, %v; want %+v", e.Snapshot(), err, s)
			}
		})
	}
}

func TestNewRejectsInvalidEntries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ledger.Snapshot)
		want   string
	}{
		{"missing id", func(s *ledger.Snapshot) { s.ID = uuid.Nil }, "id: REQUIRED"},
		{"missing wallet", func(s *ledger.Snapshot) { s.WalletID = uuid.Nil }, "walletId: REQUIRED"},
		{"missing transaction", func(s *ledger.Snapshot) { s.TransactionID = uuid.Nil }, "transactionId: REQUIRED"},
		{"unknown direction", func(s *ledger.Snapshot) { s.Direction = "debit" }, "direction: INVALID_VALUE"},
		{"zero amount", func(s *ledger.Snapshot) { s.Amount = amount(t, 0, money.BRL) }, "amount: INVALID_AMOUNT"},
		{"negative amount", func(s *ledger.Snapshot) { s.Amount = amount(t, -2500, money.BRL) }, "amount: INVALID_AMOUNT"},
		{"uninitialized amount", func(s *ledger.Snapshot) { s.Amount = money.Money{} }, "amount: REQUIRED"},
		{"negative before", func(s *ledger.Snapshot) { s.BalanceBefore = amount(t, -1, money.BRL) }, "balanceBefore: INVALID_AMOUNT"},
		{"uninitialized before", func(s *ledger.Snapshot) { s.BalanceBefore = money.Money{} }, "balanceBefore: REQUIRED"},
		{"negative after", func(s *ledger.Snapshot) {
			s.BalanceBefore, s.BalanceAfter = amount(t, 1000, money.BRL), amount(t, -1500, money.BRL)
		}, "balanceAfter: INVALID_AMOUNT"},
		{"uninitialized after", func(s *ledger.Snapshot) { s.BalanceAfter = money.Money{} }, "balanceAfter: REQUIRED"},
		{"version zero", func(s *ledger.Snapshot) { s.WalletVersion = 0 }, "walletVersion: INVALID_VALUE"},
		{"missing created at", func(s *ledger.Snapshot) { s.CreatedAt = time.Time{} }, "createdAt: REQUIRED"},
		{"wrong arithmetic", func(s *ledger.Snapshot) { s.BalanceAfter = amount(t, 8000, money.BRL) }, "balanceAfter: INVALID_VALUE"},
		{"wrong direction", func(s *ledger.Snapshot) { s.Direction = ledger.Credit }, "balanceAfter: INVALID_VALUE"},
		{"after in another currency", func(s *ledger.Snapshot) { s.BalanceAfter = amount(t, 7500, money.USD) }, "balanceAfter: INVALID_VALUE"},
		{"amount in another currency", func(s *ledger.Snapshot) { s.Amount = amount(t, 2500, money.USD) }, "balanceAfter: INVALID_VALUE"},
		{"credit overflow", func(s *ledger.Snapshot) {
			s.Direction = ledger.Credit
			s.Amount = amount(t, 1, money.BRL)
			s.BalanceBefore, s.BalanceAfter = amount(t, math.MaxInt64, money.BRL), amount(t, math.MaxInt64, money.BRL)
		}, "balanceAfter: INVALID_VALUE"},
		{"several fields", func(s *ledger.Snapshot) { s.ID, s.WalletVersion = uuid.Nil, 0 }, "id: REQUIRED\nwalletVersion: INVALID_VALUE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := debit(t)
			tt.mutate(&s)
			e, err := ledger.New(s)
			if err == nil || err.Error() != tt.want || e != (ledger.Entry{}) {
				t.Fatalf("New = %+v, %v; want error %q", e.Snapshot(), err, tt.want)
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Category != domain.Invalid {
				t.Fatalf("error %v is not a domain Invalid error", err)
			}
		})
	}
}

func TestEntryID(t *testing.T) {
	id := ledger.EntryID(transactionID)
	if id != ledger.EntryID(transactionID) || id.Version() != 5 {
		t.Fatalf("EntryID(%s) = %s; want a stable version 5 UUID", transactionID, id)
	}
	if id == ledger.EntryID(walletID) || id == transactionID {
		t.Fatalf("EntryID(%s) = %s collides", transactionID, id)
	}
}
