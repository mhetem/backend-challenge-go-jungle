//go:build integration

package usecase_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

func TestReplayReturnsTheOriginalBalance(t *testing.T) {
	t.Parallel()
	h := setup(t)
	ctx := t.Context()
	w := h.open(t, "100.00")
	bet := request(w, wager.Bet, "bet-1", "25.00", "")
	first := h.submit(t, bet)
	h.submit(t, request(w, wager.Win, "win-1", "10.00", ""))

	replay := h.submit(t, bet)
	if !replay.IdempotentReplay || replay.Transaction.ID != first.Transaction.ID || replay.Transaction.Result.Balance.String() != "75.00" {
		t.Fatalf("replay = %+v; want %s replayed at 75.00", replay, first.Transaction.ID)
	}
	if got := h.balance(t, w.ID); got != "85.00" {
		t.Fatalf("balance = %s; want 85.00", got)
	}

	byID, err := h.wagers.Get(ctx, first.Transaction.ID)
	must(t, err)
	requireEqual(t, byID, first.Transaction)
	byExternalID, err := h.wagers.GetByExternalID(ctx, "provider-a", "bet-1")
	must(t, err)
	requireEqual(t, byExternalID, first.Transaction)
	if _, err := h.wagers.Get(ctx, uuid.New()); !errors.Is(err, domain.ErrTransactionNotFound) {
		t.Fatalf("unknown id: err = %v; want %v", err, domain.ErrTransactionNotFound)
	}
	if _, err := h.wagers.GetByExternalID(ctx, "provider-b", "bet-1"); !errors.Is(err, domain.ErrTransactionNotFound) {
		t.Fatalf("external id under another provider: err = %v; want %v", err, domain.ErrTransactionNotFound)
	}
}

func TestIdempotencyConflicts(t *testing.T) {
	t.Parallel()
	h := setup(t)
	ctx := t.Context()
	w := h.open(t, "100.00")
	bet := request(w, wager.Bet, "bet-1", "25.00", "")
	h.submit(t, bet)

	changed := bet
	changed.Money.Amount = "30.00"
	if _, err := h.wagers.Submit(ctx, command(t, changed)); !errors.Is(err, app.ErrIdempotencyKeyReused) {
		t.Fatalf("same key, other payload: err = %v; want %v", err, app.ErrIdempotencyKeyReused)
	}
	otherKey, err := bet.Command("provider-a:bet-1:retry", "corr-retry")
	must(t, err)
	if _, err := h.wagers.Submit(ctx, otherKey); !errors.Is(err, app.ErrExternalIDConflict) {
		t.Fatalf("same external id, other key: err = %v; want %v", err, app.ErrExternalIDConflict)
	}
	if got := h.balance(t, w.ID); got != "75.00" {
		t.Fatalf("balance after conflicts = %s; want 75.00", got)
	}

	otherProvider := bet
	otherProvider.ProviderID = "provider-b"
	if r := h.submit(t, otherProvider); r.IdempotentReplay || r.Transaction.Status != wager.Processed {
		t.Fatalf("provider-b's own bet-1 = %+v; want a fresh PROCESSED", r)
	}
	if got := h.balance(t, w.ID); got != "50.00" {
		t.Fatalf("balance = %s; want 50.00", got)
	}
}

func TestWalletNotFoundIsRecorded(t *testing.T) {
	t.Parallel()
	h := setup(t)
	ghost := wallet.Snapshot{ID: uuid.New(), PlayerID: uuid.New()}
	bet := request(ghost, wager.Bet, "bet-1", "25.00", "")

	first := h.submit(t, bet)
	if s := first.Transaction; s.Status != wager.Rejected || s.FailureCode != wager.WalletNotFound || s.Result != nil {
		t.Fatalf("bet on a missing wallet = %+v; want REJECTED WALLET_NOT_FOUND without a balance", s)
	}
	again := h.submit(t, bet)
	if !again.IdempotentReplay {
		t.Fatal("resending the rejected bet was not a replay")
	}
	requireEqual(t, again.Transaction, first.Transaction)
}

func TestReversalMatrix(t *testing.T) {
	t.Parallel()
	h := setup(t)
	w := h.open(t, "100.00")
	steps := []struct {
		kind    wager.Kind
		extID   string
		amount  string
		ref     string
		round   string
		status  wager.Status
		code    wager.FailureCode
		balance string
	}{
		{wager.Bet, "bet-1", "30.00", "", "", wager.Processed, "", "70.00"},
		{wager.Refund, "refund-1", "30.00", "bet-1", "", wager.Processed, "", "100.00"},
		{wager.Rollback, "rollback-1", "30.00", "bet-1", "", wager.Rejected, wager.AlreadyReversed, "100.00"},
		{wager.Refund, "refund-2", "30.00", "bet-1", "", wager.Rejected, wager.AlreadyReversed, "100.00"},
		{wager.Rollback, "rollback-2", "30.00", "refund-1", "", wager.Processed, "", "70.00"},
		{wager.Rollback, "rollback-3", "30.00", "refund-1", "", wager.Rejected, wager.AlreadyReversed, "70.00"},
		{wager.Refund, "refund-3", "30.00", "bet-1", "", wager.Rejected, wager.AlreadyReversed, "70.00"},
		{wager.Rollback, "rollback-4", "30.00", "rollback-2", "", wager.Rejected, wager.ReferenceKindNotReversible, "70.00"},
		{wager.Bet, "bet-2", "20.00", "", "", wager.Processed, "", "50.00"},
		{wager.Rollback, "rollback-5", "20.00", "bet-2", "", wager.Processed, "", "70.00"},
		{wager.Refund, "refund-4", "20.00", "bet-2", "", wager.Rejected, wager.AlreadyReversed, "70.00"},
		{wager.Win, "win-1", "50.00", "", "", wager.Processed, "", "120.00"},
		{wager.Refund, "refund-5", "50.00", "win-1", "", wager.Rejected, wager.ReferenceKindNotReversible, "120.00"},
		{wager.Bet, "bet-3", "100.00", "", "", wager.Processed, "", "20.00"},
		{wager.Rollback, "rollback-6", "50.00", "win-1", "", wager.Rejected, wager.ReversalInsufficientFunds, "20.00"},
		{wager.Refund, "refund-6", "10.00", "bet-3", "", wager.Rejected, wager.ReferenceAmountMismatch, "20.00"},
		{wager.Bet, "bet-4", "500.00", "", "", wager.Rejected, wager.InsufficientFunds, "20.00"},
		{wager.Refund, "refund-7", "500.00", "bet-4", "", wager.Rejected, wager.ReferenceNotProcessed, "20.00"},
		{wager.Refund, "refund-8", "100.00", "bet-3", "", wager.Processed, "", "120.00"},
		{wager.Loss, "loss-1", "0.00", "", "", wager.Processed, "", "120.00"},
		{wager.Rollback, "rollback-7", "10.00", "loss-1", "", wager.Rejected, wager.ReferenceKindNotReversible, "120.00"},
		{wager.Win, "win-2", "15.00", "bet-3", "", wager.Processed, "", "135.00"},
		{wager.Win, "win-3", "15.00", "win-1", "", wager.Rejected, wager.ReferenceMismatch, "135.00"},
		{wager.Bet, "bet-5", "10.00", "", "", wager.Processed, "", "125.00"},
		{wager.Refund, "refund-9", "10.00", "bet-5", "round-2", wager.Rejected, wager.ReferenceMismatch, "125.00"},
		{wager.Refund, "refund-10", "10.00", "bet-9", "", wager.PendingReference, "", "125.00"},
	}
	for _, step := range steps {
		r := request(w, step.kind, step.extID, step.amount, step.ref)
		if step.round != "" {
			r.RoundID = step.round
		}
		got := h.submit(t, r).Transaction
		if got.Status != step.status || got.FailureCode != step.code {
			t.Fatalf("%s %s -> %s = %s %s; want %s %s", step.kind, step.extID, step.ref, got.Status, got.FailureCode, step.status, step.code)
		}
		if balance := h.balance(t, w.ID); balance != step.balance {
			t.Fatalf("balance after %s = %s; want %s", step.extID, balance, step.balance)
		}
	}

	if n := len(h.entries(t, w.ID)); n != 11 {
		t.Fatalf("%d ledger entries; want the opening and 10 movements", n)
	}
	r, err := h.wallets.Reconcile(t.Context(), w.ID)
	must(t, err)
	if !r.Consistent || r.CalculatedBalance.String() != "125.00" || r.PostedBalance.String() != "125.00" {
		t.Fatalf("reconciliation = %+v; want consistent at 125.00", r)
	}
	h.requireConsistentBooks(t)
}

func TestPendingReferenceIsWokenByItsReference(t *testing.T) {
	t.Parallel()
	h := setup(t)
	w := h.open(t, "100.00")
	rollback := request(w, wager.Rollback, "rollback-1", "25.00", "bet-1")

	pending := h.submit(t, rollback).Transaction
	if pending.Status != wager.PendingReference || pending.Result != nil || pending.Attempts != 1 ||
		!pending.NextAttemptAt.Equal(t0.Add(time.Second)) || !pending.ReferenceDeadlineAt.Equal(t0.Add(15*time.Minute)) {
		t.Fatalf("rollback before its bet = %+v; want PENDING_REFERENCE retried in 1s within 15m", pending)
	}
	if again := h.submit(t, rollback); !again.IdempotentReplay || again.Transaction.Status != wager.PendingReference {
		t.Fatalf("resending the pending rollback = %+v; want a PENDING_REFERENCE replay", again)
	}

	h.submit(t, request(w, wager.Bet, "bet-1", "25.00", ""))
	woken, err := h.wagers.Get(t.Context(), pending.ID)
	must(t, err)
	if woken.Status != wager.PendingReference || !woken.NextAttemptAt.Equal(t0) {
		t.Fatalf("rollback after its bet arrived = %s next at %s; want PENDING_REFERENCE due at %s", woken.Status, woken.NextAttemptAt, t0)
	}
	if got := h.balance(t, w.ID); got != "75.00" {
		t.Fatalf("balance = %s; want 75.00 until the resolver runs", got)
	}
}
