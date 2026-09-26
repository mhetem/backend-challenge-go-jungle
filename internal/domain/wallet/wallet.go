package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

type Snapshot struct {
	ID        uuid.UUID
	PlayerID  uuid.UUID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Wallet struct {
	s Snapshot
}

func OpeningTransactionID(walletID uuid.UUID) uuid.UUID {
	return domain.DeriveID("opening", walletID.String())
}

func New(id, playerID uuid.UUID, initial money.Money, now time.Time) (*Wallet, *ledger.Entry, error) {
	var v domain.Validation
	v.Check(id != uuid.Nil, "id", domain.ErrRequired)
	v.Check(playerID != uuid.Nil, "playerId", domain.ErrRequired)
	v.CheckAmount("initialBalance", initial, domain.NonNegative)
	v.Check(!now.IsZero(), "createdAt", domain.ErrRequired)
	if err := v.Err(); err != nil {
		return nil, nil, err
	}
	cur, _ := initial.Currency()
	zero, _ := money.Zero(cur)
	w := &Wallet{s: Snapshot{ID: id, PlayerID: playerID, Balance: zero, Version: 1, CreatedAt: now, UpdatedAt: now}}
	if initial == zero {
		return w, nil, nil
	}
	w.s.Version = 0
	entry, err := w.Credit(OpeningTransactionID(id), initial, now)
	if err != nil {
		return nil, nil, err
	}
	return w, &entry, nil
}

func Rehydrate(s Snapshot) (*Wallet, error) {
	var v domain.Validation
	v.Check(s.ID != uuid.Nil, "id", domain.ErrRequired)
	v.Check(s.PlayerID != uuid.Nil, "playerId", domain.ErrRequired)
	v.CheckAmount("balance", s.Balance, domain.NonNegative)
	v.Check(s.Version >= 1, "version", domain.ErrInvalidValue)
	v.Check(!s.CreatedAt.IsZero(), "createdAt", domain.ErrRequired)
	v.Check(!s.UpdatedAt.IsZero(), "updatedAt", domain.ErrRequired)
	if err := v.Err(); err != nil {
		return nil, err
	}
	return &Wallet{s: s}, nil
}

func (w *Wallet) Snapshot() Snapshot {
	return w.s
}

func (w *Wallet) ID() uuid.UUID {
	return w.s.ID
}

func (w *Wallet) PlayerID() uuid.UUID {
	return w.s.PlayerID
}

func (w *Wallet) Balance() money.Money {
	return w.s.Balance
}

func (w *Wallet) Currency() money.Currency {
	cur, _ := w.s.Balance.Currency()
	return cur
}

func (w *Wallet) Version() int64 {
	return w.s.Version
}

func (w *Wallet) Debit(transactionID uuid.UUID, amount money.Money, now time.Time) (ledger.Entry, error) {
	return w.move(ledger.Debit, transactionID, amount, now)
}

func (w *Wallet) Credit(transactionID uuid.UUID, amount money.Money, now time.Time) (ledger.Entry, error) {
	return w.move(ledger.Credit, transactionID, amount, now)
}

func (w *Wallet) move(dir ledger.Direction, transactionID uuid.UUID, amount money.Money, now time.Time) (ledger.Entry, error) {
	if w == nil || w.s.ID == uuid.Nil {
		return ledger.Entry{}, domain.ErrUninitialized
	}
	var v domain.Validation
	v.CheckAmount("amount", amount, money.Money.IsPositive)
	if err := v.Err(); err != nil {
		return ledger.Entry{}, err
	}
	if cur, _ := amount.Currency(); cur != w.Currency() {
		return ledger.Entry{}, fmt.Errorf("%w: wallet holds %s, got %s", domain.ErrCurrencyMismatch, w.Currency(), cur)
	}
	after, err := w.next(dir, amount)
	if err != nil {
		return ledger.Entry{}, err
	}
	entry, err := ledger.New(ledger.Snapshot{
		ID:            ledger.EntryID(transactionID),
		WalletID:      w.s.ID,
		TransactionID: transactionID,
		Direction:     dir,
		Amount:        amount,
		BalanceBefore: w.s.Balance,
		BalanceAfter:  after,
		WalletVersion: w.s.Version + 1,
		CreatedAt:     now,
	})
	if err != nil {
		return ledger.Entry{}, err
	}
	w.s.Balance, w.s.Version, w.s.UpdatedAt = after, w.s.Version+1, now
	return entry, nil
}

func (w *Wallet) next(dir ledger.Direction, amount money.Money) (money.Money, error) {
	if dir == ledger.Debit {
		if c, _ := w.s.Balance.Cmp(amount); c < 0 {
			return money.Money{}, fmt.Errorf("%w: balance %s, debit %s", domain.ErrInsufficientFunds, w.s.Balance, amount)
		}
		return w.s.Balance.Sub(amount)
	}
	after, err := w.s.Balance.Add(amount)
	if errors.Is(err, money.ErrOverflow) {
		return money.Money{}, fmt.Errorf("%w: balance %s, credit %s", domain.ErrBalanceOverflow, w.s.Balance, amount)
	}
	return after, err
}
