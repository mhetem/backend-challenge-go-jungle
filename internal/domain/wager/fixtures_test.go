package wager_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

var (
	t0            = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	t1            = t0.Add(time.Minute)
	walletID      = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
	playerID      = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	otherWalletID = uuid.MustParse("0192f2a0-51c3-7b8e-9f4d-2c6e8a0b1d3f")
	otherPlayerID = uuid.MustParse("0192f2a0-6e2f-7a19-8c5b-4d7f9e1a3c5b")
)

func amount(t *testing.T, minor int64, cur money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(minor, cur)
	if err != nil {
		t.Fatalf("FromMinor(%d, %s): %v", minor, cur, err)
	}
	return m
}

func brl(t *testing.T, minor int64) money.Money {
	t.Helper()
	return amount(t, minor, money.BRL)
}

func txID(extID string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(extID))
}

func params(t *testing.T, extID string, kind wager.Kind, minor int64, ref string) wager.ExternalParams {
	t.Helper()
	return wager.ExternalParams{
		Payload: wager.Payload{
			ProviderID:                     "provider-a",
			ExternalTransactionID:          extID,
			PlayerID:                       playerID,
			WalletID:                       walletID,
			RoundID:                        "round-987",
			GameID:                         "fortune-chimp",
			Kind:                           kind,
			Money:                          brl(t, minor),
			ReferenceExternalTransactionID: ref,
		},
		ID:             txID(extID),
		IdempotencyKey: "provider-a:" + extID,
		PayloadHash:    "hash-" + extID,
		CorrelationID:  "corr-" + extID,
	}
}

func pending(t *testing.T, p wager.ExternalParams) *wager.Transaction {
	t.Helper()
	tx, err := wager.NewExternal(p, t0)
	if err != nil {
		t.Fatalf("NewExternal(%s): %v", p.ExternalTransactionID, err)
	}
	return tx
}

func stored(t *testing.T, p wager.ExternalParams, status wager.Status) *wager.Transaction {
	t.Helper()
	s := pending(t, p).Snapshot()
	s.Status = status
	switch status {
	case wager.PendingReference:
		s.Attempts, s.NextAttemptAt, s.ReferenceDeadlineAt = 1, t0.Add(time.Second), t0.Add(15*time.Minute)
	case wager.Processed:
		s.Result, s.CompletedAt = &wager.Result{Balance: p.Money, WalletVersion: 2}, t0
	case wager.Rejected:
		s.FailureCode, s.CompletedAt = wager.InsufficientFunds, t0
	case wager.Failed:
		s.FailureCode, s.CompletedAt = wager.ProcessingFailed, t0
	}
	tx, err := wager.Rehydrate(s)
	if err != nil {
		t.Fatalf("Rehydrate(%s %s %s): %v", p.ExternalTransactionID, p.Kind, status, err)
	}
	return tx
}

func holder(t *testing.T, minor int64) *wallet.Wallet {
	t.Helper()
	w, err := wallet.Rehydrate(wallet.Snapshot{
		ID:        walletID,
		PlayerID:  playerID,
		Balance:   brl(t, minor),
		Version:   5,
		CreatedAt: t0,
		UpdatedAt: t0,
	})
	if err != nil {
		t.Fatalf("wallet.Rehydrate: %v", err)
	}
	return w
}

func rules(t *testing.T) wager.Rules {
	t.Helper()
	r, err := wager.NewRules(15*time.Minute, 20, func(attempt int) time.Duration {
		return time.Duration(attempt) * time.Second
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func eventID(transactionID uuid.UUID, typ events.Type) uuid.UUID {
	return domain.DeriveID("event", transactionID.String(), string(typ))
}

func eventTypes(evs []events.Event) []events.Type {
	var types []events.Type
	for _, e := range evs {
		types = append(types, e.EventHeader().EventType)
	}
	return types
}
