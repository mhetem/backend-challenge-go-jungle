package wager

import (
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

type Kind string

const (
	Opening  Kind = "OPENING"
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

type Origin string

const (
	Internal Origin = "INTERNAL"
	External Origin = "EXTERNAL"
)

func (k Kind) Valid() bool {
	switch k {
	case Opening, Bet, Win, Loss, Refund, Rollback:
		return true
	}
	return false
}

func (k Kind) Origin() Origin {
	if k == Opening {
		return Internal
	}
	return External
}

func (k Kind) Reversal() bool {
	return k == Refund || k == Rollback
}

func (k Kind) counterparty() ledger.Account {
	if k == Opening {
		return ledger.Funding
	}
	return ledger.GamingRevenue
}

func (k Kind) acceptsReference() bool {
	return k == Win || k.Reversal()
}

func (k Kind) canReference(ref Kind) bool {
	switch k {
	case Win, Refund:
		return ref == Bet
	case Rollback:
		return ref == Bet || ref == Win || ref == Refund
	}
	return false
}

func (k Kind) amountRule() func(money.Money) (bool, error) {
	if k == Loss {
		return money.Money.IsZero
	}
	return money.Money.IsPositive
}
