package ledger

import (
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

func (d Direction) Valid() bool {
	return d == Debit || d == Credit
}

type Snapshot struct {
	ID            uuid.UUID
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	WalletVersion int64
	CreatedAt     time.Time
}

type Entry struct {
	s Snapshot
}

func EntryID(transactionID uuid.UUID) uuid.UUID {
	return domain.DeriveID("ledger", transactionID.String())
}

func New(s Snapshot) (Entry, error) {
	var v domain.Validation
	v.Check(s.ID != uuid.Nil, "id", domain.ErrRequired)
	v.Check(s.WalletID != uuid.Nil, "walletId", domain.ErrRequired)
	v.Check(s.TransactionID != uuid.Nil, "transactionId", domain.ErrRequired)
	v.Check(s.Direction.Valid(), "direction", domain.ErrInvalidValue)
	v.CheckAmount("amount", s.Amount, money.Money.IsPositive)
	v.CheckAmount("balanceBefore", s.BalanceBefore, domain.NonNegative)
	v.CheckAmount("balanceAfter", s.BalanceAfter, domain.NonNegative)
	v.Check(s.WalletVersion >= 1, "walletVersion", domain.ErrInvalidValue)
	v.Check(!s.CreatedAt.IsZero(), "createdAt", domain.ErrRequired)
	if len(v) == 0 {
		want, err := s.expectedAfter()
		v.Check(err == nil && want == s.BalanceAfter, "balanceAfter", domain.ErrInvalidValue)
	}
	if err := v.Err(); err != nil {
		return Entry{}, err
	}
	return Entry{s: s}, nil
}

func (e Entry) Snapshot() Snapshot {
	return e.s
}

func (s Snapshot) expectedAfter() (money.Money, error) {
	if s.Direction == Debit {
		return s.BalanceBefore.Sub(s.Amount)
	}
	return s.BalanceBefore.Add(s.Amount)
}
