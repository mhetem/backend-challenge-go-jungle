package wager

import (
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

type Opened struct {
	Wallet      *wallet.Wallet
	Transaction *Transaction
	Entry       *ledger.Entry
	Journal     ledger.Journal
	Events      []events.Event
}

func Open(walletID, playerID uuid.UUID, initial money.Money, correlationID string, now time.Time) (Opened, error) {
	w, entry, err := wallet.New(walletID, playerID, initial, now)
	if err != nil {
		return Opened{}, err
	}
	if entry == nil {
		return Opened{Wallet: w}, nil
	}
	tx, err := NewOpening(walletID, playerID, initial, correlationID, now)
	if err != nil {
		return Opened{}, err
	}
	journal, err := ledger.Transfer(*entry, Opening.counterparty())
	if err != nil {
		return Opened{}, err
	}
	return Opened{Wallet: w, Transaction: tx, Entry: entry, Journal: journal, Events: tx.processedEvents(entry, now)}, nil
}
