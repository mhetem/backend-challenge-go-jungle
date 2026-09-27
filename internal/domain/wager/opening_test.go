package wager_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

func TestOpenWithBalance(t *testing.T) {
	opened, err := wager.Open(walletID, playerID, brl(t, 100000), "corr-open", t0)
	if err != nil {
		t.Fatal(err)
	}
	wantWallet := wallet.Snapshot{ID: walletID, PlayerID: playerID, Balance: brl(t, 100000), Version: 1, CreatedAt: t0, UpdatedAt: t0}
	if opened.Wallet == nil || opened.Wallet.Snapshot() != wantWallet {
		t.Fatalf("wallet = %+v; want %+v", opened.Wallet, wantWallet)
	}
	id := wallet.OpeningTransactionID(walletID)
	wantTx := wager.Snapshot{
		ID:            id,
		WalletID:      walletID,
		PlayerID:      playerID,
		Kind:          wager.Opening,
		Money:         brl(t, 100000),
		CorrelationID: "corr-open",
		Status:        wager.Processed,
		Result:        &wager.Result{Balance: brl(t, 100000), WalletVersion: 1},
		CreatedAt:     t0,
		UpdatedAt:     t0,
		CompletedAt:   t0,
	}
	if opened.Transaction == nil || !reflect.DeepEqual(opened.Transaction.Snapshot(), wantTx) {
		t.Fatalf("transaction = %+v; want %+v", opened.Transaction, wantTx)
	}
	wantEntry := ledger.Snapshot{
		ID:            ledger.EntryID(id),
		WalletID:      walletID,
		TransactionID: id,
		Direction:     ledger.Credit,
		Amount:        brl(t, 100000),
		BalanceBefore: brl(t, 0),
		BalanceAfter:  brl(t, 100000),
		WalletVersion: 1,
		CreatedAt:     t0,
	}
	if opened.Entry == nil || opened.Entry.Snapshot() != wantEntry {
		t.Fatalf("entry = %+v; want %+v", opened.Entry, wantEntry)
	}
	wantJournal := []ledger.Posting{
		{WalletID: walletID, TransactionID: id, Account: ledger.PlayerBalances, Direction: ledger.Credit, Amount: brl(t, 100000), CreatedAt: t0},
		{WalletID: walletID, TransactionID: id, Account: ledger.Funding, Direction: ledger.Debit, Amount: brl(t, 100000), CreatedAt: t0},
	}
	if got := opened.Journal.Postings(); !reflect.DeepEqual(got, wantJournal) {
		t.Fatalf("journal = %+v; want %+v", got, wantJournal)
	}
	wantEvents := []events.Event{
		events.NewWagerTransactionProcessed(events.Meta{
			EventID:       eventID(id, events.TypeWagerTransactionProcessed),
			CorrelationID: "corr-open",
			OccurredAt:    t0,
		}, events.WagerTransactionProcessedData{
			TransactionData: events.TransactionData{
				TransactionID: id,
				Origin:        "INTERNAL",
				WalletID:      walletID,
				PlayerID:      playerID,
				Kind:          "OPENING",
				Money:         brl(t, 100000),
			},
			Balance:       brl(t, 100000),
			WalletVersion: 1,
		}),
		events.NewWalletBalanceChanged(events.Meta{
			EventID:       eventID(id, events.TypeWalletBalanceChanged),
			CorrelationID: "corr-open",
			CausationID:   id.String(),
			OccurredAt:    t0,
		}, events.WalletBalanceChangedData{
			WalletID:      walletID,
			TransactionID: id,
			Direction:     "CREDIT",
			Money:         brl(t, 100000),
			BalanceBefore: brl(t, 0),
			BalanceAfter:  brl(t, 100000),
			WalletVersion: 1,
		}),
	}
	if !reflect.DeepEqual(opened.Events, wantEvents) {
		t.Fatalf("events = %+v; want %+v", opened.Events, wantEvents)
	}
	if _, err := wager.Rehydrate(opened.Transaction.Snapshot()); err != nil {
		t.Fatalf("opening does not rehydrate: %v", err)
	}
}

func TestOpenWithZeroBalance(t *testing.T) {
	opened, err := wager.Open(walletID, playerID, amount(t, 0, money.USD), "corr-open", t0)
	if err != nil {
		t.Fatal(err)
	}
	want := wallet.Snapshot{ID: walletID, PlayerID: playerID, Balance: amount(t, 0, money.USD), Version: 1, CreatedAt: t0, UpdatedAt: t0}
	if opened.Wallet == nil || opened.Wallet.Snapshot() != want {
		t.Fatalf("wallet = %+v; want %+v", opened.Wallet, want)
	}
	if opened.Transaction != nil || opened.Entry != nil || opened.Journal.Postings() != nil || opened.Events != nil {
		t.Fatalf("zero opening produced %+v", opened)
	}
}

func TestOpenRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name          string
		walletID      uuid.UUID
		initial       money.Money
		correlationID string
		want          string
	}{
		{"missing wallet", uuid.Nil, brl(t, 100), "corr-open", "id: REQUIRED"},
		{"negative initial", walletID, brl(t, -100), "corr-open", "initialBalance: INVALID_AMOUNT"},
		{"credit without correlation", walletID, brl(t, 100), "", "correlationId: REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opened, err := wager.Open(tt.walletID, playerID, tt.initial, tt.correlationID, t0)
			if err == nil || err.Error() != tt.want || !reflect.DeepEqual(opened, wager.Opened{}) {
				t.Fatalf("Open = %+v, %v; want error %q", opened, err, tt.want)
			}
		})
	}
}
