//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

func TestPausedPostgres(t *testing.T) {
	c := newCluster(t, nil)
	a, b := c.start("a", nil), c.start("b", nil)
	w := c.open(a, "100.00")

	c.pause("postgres")
	eventually(t, 15*time.Second, "readiness to report the database", func() bool {
		return c.ready(a) == http.StatusServiceUnavailable && c.ready(b) == http.StatusServiceUnavailable
	})
	blocked := op{extID: "bet-1", kind: "BET", amount: "25.00"}
	if r, err := c.try(a, w, blocked); err != nil || r.status != http.StatusServiceUnavailable || r.str("code") != "TEMPORARILY_UNAVAILABLE" {
		t.Fatalf("bet while postgres is paused = %v %d %s; want 503 TEMPORARILY_UNAVAILABLE", err, r.status, r.raw)
	}
	c.send(w, "msg-1", "dedup-1", op{extID: "bet-2", kind: "BET", amount: "5.00"})
	eventually(t, 15*time.Second, "the consumers to pause", func() bool {
		return c.metric(a, "wallet_consumer_paused") == 1 && c.metric(b, "wallet_consumer_paused") == 1
	})
	time.Sleep(2 * time.Second)
	if visible, inFlight := c.queues.Counts(c.queues.InputURL); visible != 1 || inFlight != 0 {
		t.Fatalf("input queue: %d visible, %d in flight; want msg-1 left alone while the database is down", visible, inFlight)
	}

	c.unpause("postgres")
	eventually(t, 30*time.Second, "readiness to recover", func() bool {
		return c.ready(a) == http.StatusOK && c.ready(b) == http.StatusOK
	})
	c.submit(b, w, blocked, http.StatusOK)
	eventually(t, 20*time.Second, "msg-1", func() bool { outcome, _ := c.inbox("msg-1"); return outcome == "PROCESSED" })
	eventually(t, 10*time.Second, "the consumers to resume", func() bool {
		return c.metric(a, "wallet_consumer_paused") == 0 && c.metric(b, "wallet_consumer_paused") == 0
	})
	c.requireQueuesDrained()
	if n := c.debits(w); n != 2 {
		t.Fatalf("%d debits; want 2", n)
	}
	c.reconcile(a, w, "70.00")
}

func TestPausedSQS(t *testing.T) {
	c := newCluster(t, nil)
	a, b := c.start("a", nil), c.start("b", nil)
	w := c.open(a, "100.00")
	c.events(2)

	c.pause("aws")
	eventually(t, 15*time.Second, "readiness to report SQS", func() bool {
		return c.ready(a) == http.StatusServiceUnavailable && c.ready(b) == http.StatusServiceUnavailable
	})
	bet := c.submit(b, w, op{extID: "bet-1", kind: "BET", amount: "25.00"}, http.StatusOK)
	time.Sleep(2 * time.Second)
	if n := c.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`); n != 2 {
		t.Fatalf("%d events waiting while SQS is paused; want the bet's two", n)
	}

	c.unpause("aws")
	eventually(t, 30*time.Second, "readiness to recover", func() bool {
		return c.ready(a) == http.StatusOK && c.ready(b) == http.StatusOK
	})
	eventually(t, 30*time.Second, "the outbox to drain", func() bool {
		return c.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
	evs := c.events(2)
	if got := kinds(evs); got["WagerTransactionProcessed"] != 1 || got["WalletBalanceChanged"] != 1 {
		t.Fatalf("events by type %v; want the bet's processed and balance-changed events", got)
	}
	for _, e := range evs {
		if e.Type == "WagerTransactionProcessed" && e.AggregateID != bet.str("transactionId") {
			t.Fatalf("processed event %+v; want it on the bet %s", e, bet.str("transactionId"))
		}
	}
	c.reconcile(a, w, "75.00")
}
