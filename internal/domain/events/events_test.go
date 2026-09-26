package events_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

var (
	brt           = time.FixedZone("BRT", -3*60*60)
	occurredAt    = time.Date(2026, 9, 26, 9, 0, 0, 123000000, brt)
	eventID       = uuid.MustParse("0192f2a1-7c3e-7d4a-9b5f-1e2d3c4b5a69")
	correlationID = "0192f2a2-9c1b-7e2a-8f00-5b7d3c2a1e90"
	transactionID = uuid.MustParse("0192f298-345e-7e38-af88-e43f851a819d")
	rollbackID    = uuid.MustParse("0192f29a-5b6c-7d8e-9f01-2a3b4c5d6e7f")
	openingID     = uuid.MustParse("5c1f0b8e-2d3a-5e4f-8a6b-7c8d9e0f1a2b")
	walletID      = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
	playerID      = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
)

func brl(t *testing.T, minor int64) money.Money {
	t.Helper()
	m, err := money.FromMinor(minor, money.BRL)
	if err != nil {
		t.Fatalf("FromMinor(%d): %v", minor, err)
	}
	return m
}

func meta() events.Meta {
	return events.Meta{EventID: eventID, CorrelationID: correlationID, OccurredAt: occurredAt}
}

func bet(t *testing.T) events.TransactionData {
	return events.TransactionData{
		TransactionID:         transactionID,
		Origin:                "EXTERNAL",
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 brl(t, 2500),
	}
}

func rollback(t *testing.T) events.TransactionData {
	d := bet(t)
	d.TransactionID = rollbackID
	d.ExternalTransactionID = "transaction-124"
	d.Kind = "ROLLBACK"
	d.ReferenceExternalTransactionID = "transaction-123"
	return d
}

func samples(t *testing.T) map[string]events.Event {
	resolved := rollback(t)
	resolved.ReferenceTransactionID = transactionID
	overdrawn := bet(t)
	overdrawn.Money = brl(t, 8000)
	caused := meta()
	caused.CausationID = transactionID.String()
	return map[string]events.Event{
		"wager_transaction_processed": events.NewWagerTransactionProcessed(meta(), events.WagerTransactionProcessedData{
			TransactionData: bet(t),
			Balance:         brl(t, 97500),
			WalletVersion:   2,
		}),
		"wager_transaction_processed_reversal": events.NewWagerTransactionProcessed(meta(), events.WagerTransactionProcessedData{
			TransactionData: resolved,
			Balance:         brl(t, 100000),
			WalletVersion:   3,
		}),
		"wager_transaction_processed_opening": events.NewWagerTransactionProcessed(meta(), events.WagerTransactionProcessedData{
			TransactionData: events.TransactionData{
				TransactionID: openingID,
				Origin:        "INTERNAL",
				WalletID:      walletID,
				PlayerID:      playerID,
				Kind:          "OPENING",
				Money:         brl(t, 100000),
			},
			Balance:       brl(t, 100000),
			WalletVersion: 1,
		}),
		"wager_transaction_rejected": events.NewWagerTransactionRejected(meta(), events.WagerTransactionRejectedData{
			TransactionData: overdrawn,
			FailureCode:     "INSUFFICIENT_FUNDS",
			Balance:         brl(t, 2000),
			WalletVersion:   3,
		}),
		"wager_transaction_rejected_without_balance": events.NewWagerTransactionRejected(meta(), events.WagerTransactionRejectedData{
			TransactionData: bet(t),
			FailureCode:     "WALLET_NOT_FOUND",
		}),
		"wager_transaction_pending_reference": events.NewWagerTransactionPendingReference(meta(), events.WagerTransactionPendingReferenceData{
			TransactionData:     rollback(t),
			ReferenceDeadlineAt: time.Date(2026, 9, 26, 9, 15, 0, 0, brt),
		}),
		"wallet_balance_changed": events.NewWalletBalanceChanged(caused, events.WalletBalanceChangedData{
			WalletID:      walletID,
			TransactionID: transactionID,
			Direction:     "DEBIT",
			Money:         brl(t, 2500),
			BalanceBefore: brl(t, 100000),
			BalanceAfter:  brl(t, 97500),
			WalletVersion: 2,
		}),
	}
}

func TestGoldenJSON(t *testing.T) {
	for name, event := range samples(t) {
		t.Run(name, func(t *testing.T) {
			got, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", name+".json")
			if *update {
				var indented bytes.Buffer
				if err := json.Indent(&indented, got, "", "  "); err != nil {
					t.Fatal(err)
				}
				indented.WriteByte('\n')
				if err := os.WriteFile(path, indented.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			golden, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var want bytes.Buffer
			if err := json.Compact(&want, golden); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want.Bytes()) {
				t.Fatalf("got  %s\nwant %s", got, want.Bytes())
			}
		})
	}
}

func TestConstructorsSetHeader(t *testing.T) {
	tests := []struct {
		name          string
		eventType     events.Type
		aggregateID   uuid.UUID
		aggregateType string
	}{
		{"wager_transaction_processed", events.TypeWagerTransactionProcessed, transactionID, "WagerTransaction"},
		{"wager_transaction_rejected", events.TypeWagerTransactionRejected, transactionID, "WagerTransaction"},
		{"wager_transaction_pending_reference", events.TypeWagerTransactionPendingReference, rollbackID, "WagerTransaction"},
		{"wallet_balance_changed", events.TypeWalletBalanceChanged, walletID, "Wallet"},
	}
	all := samples(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := all[tt.name]
			h := event.EventHeader()
			if h.EventID != eventID || h.EventType != tt.eventType || h.AggregateID != tt.aggregateID || h.CorrelationID != correlationID || h.Version != 1 {
				t.Fatalf("header = %+v", h)
			}
			if h.OccurredAt.Location() != time.UTC || !h.OccurredAt.Equal(occurredAt) {
				t.Fatalf("occurredAt = %s; want %s in UTC", h.OccurredAt, occurredAt)
			}
			if h.EventType.AggregateType() != tt.aggregateType || event.PartitionKey() != walletID {
				t.Fatalf("aggregate type %q, partition key %s", h.EventType.AggregateType(), event.PartitionKey())
			}
		})
	}
	pending := all["wager_transaction_pending_reference"].(events.WagerTransactionPendingReference)
	if pending.Data.ReferenceDeadlineAt.Location() != time.UTC {
		t.Fatalf("referenceDeadlineAt = %s; want UTC", pending.Data.ReferenceDeadlineAt)
	}
	if got := events.Type("Unknown").AggregateType(); got != "" {
		t.Fatalf("AggregateType of an unknown type = %q", got)
	}
}
