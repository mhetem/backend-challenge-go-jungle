//go:build e2e

package e2e_test

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/failpoint"
)

func arm(point string) map[string]string {
	return map[string]string{"FAILPOINTS": point + "=exit"}
}

func only(components string, extra map[string]string) map[string]string {
	env := map[string]string{"COMPONENTS": components}
	maps.Copy(env, extra)
	return env
}

func TestConsumerCrashAfterCommit(t *testing.T) {
	c := newCluster(t, nil)
	front := c.start("front", only("http", nil))
	w := c.open(front, "100.00")
	crashing := c.start("a", only("consumer", arm(failpoint.ConsumerAfterCommit)))
	c.send(w, "msg-1", "dedup-1", op{extID: "bet-1", kind: "BET", amount: "25.00"})
	c.expectExit(crashing, failpoint.ExitCode, 20*time.Second)

	if outcome, _ := c.inbox("msg-1"); outcome != "PROCESSED" {
		t.Fatalf("msg-1 inbox outcome %q after the crash; want PROCESSED, committed before the exit", outcome)
	}
	if visible, inFlight := c.queues.Counts(c.queues.InputURL); visible+inFlight != 1 {
		t.Fatalf("input queue holds %d messages after the crash; want msg-1, never deleted", visible+inFlight)
	}

	survivor := c.start("b", only("consumer", nil))
	eventually(t, 30*time.Second, "the redelivery to reach the survivor", func() bool { return c.metric(survivor, consumed("duplicate")) == 1 })
	if n := c.metric(survivor, consumed("processed")); n != 0 {
		t.Fatalf("the survivor processed %v messages fresh; want the redelivery recognised as a duplicate", n)
	}
	c.requireQueuesDrained()
	if n, d := c.transactions(), c.debits(w); n != 1 || d != 1 {
		t.Fatalf("%d transactions and %d debits; want one of each", n, d)
	}
	c.reconcile(front, w, "75.00")
}

func TestPublisherCrashes(t *testing.T) {
	for _, point := range []string{failpoint.OutboxAfterClaim, failpoint.OutboxAfterPublish} {
		t.Run(point, func(t *testing.T) {
			c := newCluster(t, nil)
			front := c.start("front", only("http", nil))
			crashing := c.start("a", only("outbox", arm(point)))
			c.open(front, "100.00")
			c.expectExit(crashing, failpoint.ExitCode, 20*time.Second)

			rows := c.outbox()
			if len(rows) != 2 {
				t.Fatalf("%d outbox rows; want the opening's two events", len(rows))
			}
			for _, r := range rows {
				if r.published || r.attempts != 1 || !strings.HasPrefix(r.claimedBy, "e2e-a/") {
					t.Fatalf("row %+v after the crash; want claimed once by e2e-a and not marked published", r)
				}
			}

			survivor := c.start("b", only("outbox", nil))
			eventually(t, 20*time.Second, "the survivor to publish", func() bool {
				return c.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
			})
			var ids []string
			for _, r := range c.outbox() {
				if r.attempts != 2 || !strings.HasPrefix(r.claimedBy, "e2e-b/") {
					t.Fatalf("row %+v; want taken over by e2e-b after the lease, on the second attempt", r)
				}
				ids = append(ids, r.id)
			}
			if n := c.metric(survivor, `wallet_outbox_publish_results_total{result="published"}`); n != 2 {
				t.Fatalf("the survivor published %v events; want 2", n)
			}
			var delivered []string
			for _, e := range c.events(2) {
				delivered = append(delivered, e.ID)
			}
			slices.Sort(ids)
			slices.Sort(delivered)
			if !slices.Equal(ids, delivered) {
				t.Fatalf("delivered event ids %v; want the outbox ids %v, each once", delivered, ids)
			}
		})
	}
}

func TestPendingReferenceSurvivesACrash(t *testing.T) {
	c := newCluster(t, nil)
	crashing := c.start("a", only("http", arm(failpoint.UsecaseAfterPendingReferenceCommit)))
	b, other := c.start("b", nil), c.start("c", nil)
	w := c.open(b, "100.00")

	rollback := op{extID: "rollback-1", kind: "ROLLBACK", amount: "25.00", ref: "bet-1"}
	if r, err := c.try(crashing, w, rollback); err == nil {
		t.Fatalf("rollback on the crashing instance answered %d %s; want the connection to drop", r.status, r.raw)
	}
	c.expectExit(crashing, failpoint.ExitCode, 10*time.Second)

	retried := c.submit(b, w, rollback, http.StatusAccepted)
	if retried.str("status") != "PENDING_REFERENCE" || retried.body["idempotentReplay"] != true {
		t.Fatalf("client retry on b = %s; want a replay of the PENDING_REFERENCE committed before the crash", retried.raw)
	}
	c.submit(other, w, op{extID: "bet-1", kind: "BET", amount: "25.00"}, http.StatusOK)
	eventually(t, 15*time.Second, "the rollback to settle", func() bool { return c.transaction(b, "rollback-1").str("status") == "PROCESSED" })

	evs := c.events(7)
	if got := kinds(evs); got["WagerTransactionProcessed"] != 3 || got["WalletBalanceChanged"] != 3 || got["WagerTransactionPendingReference"] != 1 {
		t.Fatalf("events by type %v; want 3 processed, 3 balance changes and 1 pending reference", got)
	}
	c.reconcile(other, w, "100.00")
}

func TestPendingReferenceExpiresAfterAResolverCrash(t *testing.T) {
	c := newCluster(t, map[string]string{"PENDING_REF_TTL": "6s"})
	front := c.start("front", only("http,outbox", nil))
	crashing := c.start("a", only("resolver", arm(failpoint.ResolverAfterReschedule)))
	w := c.open(front, "100.00")

	refund := c.submit(front, w, op{extID: "refund-1", kind: "REFUND", amount: "10.00", ref: "bet-404"}, http.StatusAccepted)
	c.expectExit(crashing, failpoint.ExitCode, 15*time.Second)
	if tx := c.transaction(front, "refund-1"); tx.str("status") != "PENDING_REFERENCE" || tx.body["attempts"] != float64(2) {
		t.Fatalf("refund after the crash = %s; want PENDING_REFERENCE with the reschedule committed (attempts 2)", tx.raw)
	}

	c.start("b", only("resolver", nil))
	eventually(t, 20*time.Second, "the refund to expire", func() bool { return c.transaction(front, "refund-1").str("status") == "REJECTED" })
	if tx := c.transaction(front, "refund-1"); tx.str("failureCode") != "REFERENCE_NOT_FOUND" {
		t.Fatalf("expired refund = %s; want REFERENCE_NOT_FOUND", tx.raw)
	}

	evs := c.events(4)
	onRefund := map[string]int{}
	for _, e := range evs {
		if e.AggregateID == refund.str("transactionId") {
			onRefund[e.Type]++
		}
	}
	if onRefund["WagerTransactionPendingReference"] != 1 || onRefund["WagerTransactionRejected"] != 1 || len(onRefund) != 2 {
		t.Fatalf("events on the refund %v; want one pending reference and one rejection", onRefund)
	}
	c.reconcile(front, w, "100.00")
}
