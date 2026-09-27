//go:build integration

package usecase_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

func TestTwoBetsRaceForTheSameFunds(t *testing.T) {
	t.Parallel()
	h := setup(t)
	w := h.open(t, "100.00")
	cmds := []app.SubmitWager{
		command(t, request(w, wager.Bet, "bet-1", "80.00", "")),
		command(t, request(w, wager.Bet, "bet-2", "80.00", "")),
	}

	first := h.submitConcurrently(t, cmds)
	var processed, rejected int
	for _, r := range first {
		s := r.Transaction
		switch {
		case r.IdempotentReplay:
			t.Fatalf("first submission of %s reported a replay", s.ExternalTransactionID)
		case s.Status == wager.Processed:
			processed++
		case s.Status == wager.Rejected && s.FailureCode == wager.InsufficientFunds:
			rejected++
		}
		if s.Result == nil || s.Result.Balance.String() != "20.00" {
			t.Fatalf("%s result = %+v; want balance 20.00", s.ExternalTransactionID, s.Result)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("%d processed and %d rejected; want one of each", processed, rejected)
	}
	if got := h.balance(t, w.ID); got != "20.00" {
		t.Fatalf("balance = %s; want 20.00", got)
	}
	if n := debits(h.entries(t, w.ID)); n != 1 {
		t.Fatalf("%d debits; want 1", n)
	}

	again := h.submitConcurrently(t, cmds)
	for i, r := range again {
		if !r.IdempotentReplay {
			t.Fatalf("resending %s was not a replay", cmds[i].ExternalTransactionID)
		}
		requireEqual(t, r.Transaction, first[i].Transaction)
	}
	if got := h.balance(t, w.ID); got != "20.00" {
		t.Fatalf("balance after resending = %s; want 20.00", got)
	}
	requireEqual(t, h.postings(t, w.ID), map[string]int{
		"FUNDING DEBIT":          1,
		"PLAYER_BALANCES CREDIT": 1,
		"PLAYER_BALANCES DEBIT":  1,
		"GAMING_REVENUE CREDIT":  1,
	})
	h.requireConsistentBooks(t)
}

func TestSameBetFiftyTimes(t *testing.T) {
	t.Parallel()
	h := setup(t)
	w := h.open(t, "100.00")
	cmd := command(t, request(w, wager.Bet, "bet-1", "25.00", ""))

	results := h.submitConcurrently(t, slices.Repeat([]app.SubmitWager{cmd}, 50))
	fresh := 0
	for _, r := range results {
		if !r.IdempotentReplay {
			fresh++
		}
		s := r.Transaction
		if s.ID != results[0].Transaction.ID || s.Status != wager.Processed || s.Result == nil || s.Result.Balance.String() != "75.00" {
			t.Fatalf("result = %+v; want PROCESSED %s at 75.00", s, results[0].Transaction.ID)
		}
	}
	if fresh != 1 {
		t.Fatalf("%d fresh results; want 1 and 49 replays", fresh)
	}
	if got := h.balance(t, w.ID); got != "75.00" {
		t.Fatalf("balance = %s; want 75.00", got)
	}
	if n := debits(h.entries(t, w.ID)); n != 1 {
		t.Fatalf("%d debits; want 1", n)
	}
	if n := h.postings(t, w.ID)["PLAYER_BALANCES DEBIT"]; n != 1 {
		t.Fatalf("%d player debits in the journal; want 1", n)
	}
	h.requireConsistentBooks(t)
}

func TestDistinctWalletsProceedInParallel(t *testing.T) {
	t.Parallel()
	h := setup(t)
	ctx := t.Context()
	a, b := h.open(t, "100.00"), h.open(t, "100.00")
	onA := command(t, request(a, wager.Bet, "bet-a", "10.00", ""))

	holder, err := h.db.App.Begin(ctx)
	must(t, err)
	defer func() { _ = holder.Rollback(context.Background()) }()
	_, err = holder.Exec(ctx, `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, a.ID)
	must(t, err)

	blocked := make(chan error, 1)
	go func() {
		_, err := h.wagers.Submit(ctx, onA)
		blocked <- err
	}()

	start := time.Now()
	onB := h.submit(t, request(b, wager.Bet, "bet-b", "10.00", ""))
	if elapsed := time.Since(start); onB.Transaction.Status != wager.Processed || elapsed > time.Second {
		t.Fatalf("bet on wallet B = %s after %s while A was locked; want PROCESSED within ms", onB.Transaction.Status, elapsed)
	}
	select {
	case err := <-blocked:
		t.Fatalf("bet on the locked wallet A finished while its lock was held: %v", err)
	default:
	}

	must(t, holder.Rollback(ctx))
	must(t, <-blocked)
	if got := h.balance(t, a.ID); got != "90.00" {
		t.Fatalf("wallet A balance = %s; want 90.00 once the lock was released", got)
	}
	h.requireConsistentBooks(t)
}
