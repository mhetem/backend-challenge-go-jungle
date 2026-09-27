package ledger

import (
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

type Account string

const (
	Funding        Account = "FUNDING"
	PlayerBalances Account = "PLAYER_BALANCES"
	GamingRevenue  Account = "GAMING_REVENUE"
)

func (a Account) Valid() bool {
	switch a {
	case Funding, PlayerBalances, GamingRevenue:
		return true
	}
	return false
}

func (a Account) Normal() Direction {
	if a == Funding {
		return Debit
	}
	return Credit
}

func (d Direction) Opposite() Direction {
	switch d {
	case Debit:
		return Credit
	case Credit:
		return Debit
	}
	return d
}

type Posting struct {
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Account       Account
	Direction     Direction
	Amount        money.Money
	CreatedAt     time.Time
}

type Journal struct {
	postings []Posting
}

func NewJournal(postings ...Posting) (Journal, error) {
	var v domain.Validation
	v.Check(len(postings) >= 2, "postings", domain.ErrInvalidValue)
	for i, p := range postings {
		p.check(&v, fmt.Sprintf("postings[%d].", i))
	}
	if len(v) == 0 {
		v.Check(oneTransaction(postings) && balanced(postings), "postings", domain.ErrInvalidValue)
	}
	if err := v.Err(); err != nil {
		return Journal{}, err
	}
	return Journal{postings: slices.Clone(postings)}, nil
}

func Transfer(e Entry, counterparty Account) (Journal, error) {
	player := Posting{
		WalletID:      e.s.WalletID,
		TransactionID: e.s.TransactionID,
		Account:       PlayerBalances,
		Direction:     e.s.Direction,
		Amount:        e.s.Amount,
		CreatedAt:     e.s.CreatedAt,
	}
	other := player
	other.Account, other.Direction = counterparty, e.s.Direction.Opposite()
	return NewJournal(player, other)
}

func (j Journal) Postings() []Posting {
	return slices.Clone(j.postings)
}

func (p Posting) check(v *domain.Validation, prefix string) {
	v.Check(p.WalletID != uuid.Nil, prefix+"walletId", domain.ErrRequired)
	v.Check(p.TransactionID != uuid.Nil, prefix+"transactionId", domain.ErrRequired)
	v.Check(p.Account.Valid(), prefix+"account", domain.ErrInvalidValue)
	v.Check(p.Direction.Valid(), prefix+"direction", domain.ErrInvalidValue)
	v.CheckAmount(prefix+"amount", p.Amount, money.Money.IsPositive)
	v.Check(!p.CreatedAt.IsZero(), prefix+"createdAt", domain.ErrRequired)
}

func oneTransaction(postings []Posting) bool {
	accounts := map[Account]bool{}
	for _, p := range postings {
		if p.WalletID != postings[0].WalletID || p.TransactionID != postings[0].TransactionID || accounts[p.Account] {
			return false
		}
		accounts[p.Account] = true
	}
	return true
}

func balanced(postings []Posting) bool {
	cur, _ := postings[0].Amount.Currency()
	debits, _ := money.Zero(cur)
	credits := debits
	for _, p := range postings {
		var err error
		if p.Direction == Debit {
			debits, err = debits.Add(p.Amount)
		} else {
			credits, err = credits.Add(p.Amount)
		}
		if err != nil {
			return false
		}
	}
	return debits == credits
}
