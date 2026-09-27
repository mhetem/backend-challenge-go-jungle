//go:build e2e

package e2e_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestTwoBetsRaceOnDifferentInstances(t *testing.T) {
	c := newCluster(t, nil)
	a, b, _ := c.start("a", nil), c.start("b", nil), c.start("c", nil)
	w := c.open(a, "100.00")
	bets := []op{{extID: "bet-1", kind: "BET", amount: "80.00"}, {extID: "bet-2", kind: "BET", amount: "80.00"}}
	targets := []*process{a, b}

	results := make([]response, len(bets))
	errs := make([]error, len(bets))
	var wg sync.WaitGroup
	for i := range bets {
		wg.Go(func() { results[i], errs[i] = c.try(targets[i], w, bets[i]) })
	}
	wg.Wait()

	processed, rejected := 0, 0
	for i, r := range results {
		switch {
		case errs[i] != nil:
			t.Fatalf("%s on %s: %v", bets[i].extID, targets[i].name, errs[i])
		case r.status == http.StatusOK && r.str("status") == "PROCESSED" && r.money("balance") == "20.00":
			processed++
		case r.status == http.StatusUnprocessableEntity && r.str("failureCode") == "INSUFFICIENT_FUNDS" && r.money("balance") == "20.00":
			rejected++
		default:
			t.Fatalf("%s on %s = %d %s", bets[i].extID, targets[i].name, r.status, r.raw)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("%d processed and %d rejected; want one of each, both seeing 20.00", processed, rejected)
	}

	for i, p := range []*process{b, a} {
		replay := c.submit(p, w, bets[i], results[i].status)
		if replay.body["idempotentReplay"] != true || replay.str("transactionId") != results[i].str("transactionId") ||
			replay.str("status") != results[i].str("status") || replay.money("balance") != "20.00" {
			t.Fatalf("replay of %s on %s = %s; want the stored result of %s", bets[i].extID, p.name, replay.raw, results[i].raw)
		}
	}
	if n := c.debits(w); n != 1 {
		t.Fatalf("%d debits; want 1", n)
	}
	c.reconcile(b, w, "20.00")
}

func TestSameBetFiftyTimesOverHTTPAndSQS(t *testing.T) {
	c := newCluster(t, nil)
	procs := []*process{c.start("a", nil), c.start("b", nil), c.start("c", nil)}
	w := c.open(procs[0], "100.00")
	bet := op{extID: "bet-1", kind: "BET", amount: "10.00"}

	for i := range 20 {
		c.send(w, fmt.Sprintf("msg-%d", i), fmt.Sprintf("dedup-%d", i), bet)
	}
	results := make([]response, 30)
	errs := make([]error, 30)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { results[i], errs[i] = c.try(procs[i%len(procs)], w, bet) })
	}
	wg.Wait()

	id, fresh := results[0].str("transactionId"), 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if r.status != http.StatusOK || r.str("transactionId") != id || r.money("balance") != "90.00" {
			t.Fatalf("request %d = %d %s; want 200 with transaction %s and balance 90.00", i, r.status, r.raw, id)
		}
		if r.body["idempotentReplay"] == false {
			fresh++
		}
	}
	eventually(t, 30*time.Second, "the 20 messages", func() bool { return c.count(`SELECT count(*) FROM inbox_messages`) == 20 })
	if n := c.count(`SELECT count(*) FROM inbox_messages WHERE transaction_id::text <> $1`, id); n != 0 {
		t.Fatalf("%d inbox rows point at another transaction; want all at %s", n, id)
	}
	processed := c.count(`SELECT count(*) FROM inbox_messages WHERE outcome = 'PROCESSED'`)
	if fresh+processed != 1 {
		t.Fatalf("%d fresh HTTP results and %d processed messages; want exactly one effect in total", fresh, processed)
	}
	if n := c.sum(consumed("processed"), procs...) + c.sum(consumed("replayed"), procs...); n != 20 {
		t.Fatalf("consumers settled %v messages as processed or replayed; want 20", n)
	}
	if n, d := c.transactions(), c.debits(w); n != 1 || d != 1 {
		t.Fatalf("%d transactions and %d debits; want one of each", n, d)
	}
	c.requireQueuesDrained()
	c.reconcile(procs[2], w, "90.00")
}

func TestManyWalletsProgressInParallel(t *testing.T) {
	c := newCluster(t, nil)
	procs := []*process{c.start("a", nil), c.start("b", nil), c.start("c", nil)}
	wallets := make([]wallet, 8)
	for i := range wallets {
		wallets[i] = c.open(procs[i%len(procs)], "100.00")
	}

	type job struct {
		w wallet
		o op
	}
	var viaHTTP []job
	for i, w := range wallets {
		for j := range 3 {
			c.send(w, fmt.Sprintf("msg-%d-%d", i, j), fmt.Sprintf("dedup-%d-%d", i, j), op{extID: fmt.Sprintf("sqs-%d-%d", i, j), kind: "BET", amount: "5.00"})
			viaHTTP = append(viaHTTP, job{w, op{extID: fmt.Sprintf("http-%d-%d", i, j), kind: "BET", amount: "5.00"}})
		}
	}
	results := make([]response, len(viaHTTP))
	errs := make([]error, len(viaHTTP))
	var wg sync.WaitGroup
	for i, j := range viaHTTP {
		wg.Go(func() { results[i], errs[i] = c.try(procs[i%len(procs)], j.w, j.o) })
	}
	wg.Wait()
	for i, r := range results {
		if errs[i] != nil || r.status != http.StatusOK || r.str("status") != "PROCESSED" {
			t.Fatalf("%s = %v %d %s; want 200 PROCESSED", viaHTTP[i].o.extID, errs[i], r.status, r.raw)
		}
	}

	eventually(t, 30*time.Second, "the 24 messages", func() bool { return c.count(`SELECT count(*) FROM inbox_messages`) == 24 })
	if n := c.count(`SELECT count(*) FROM inbox_messages WHERE outcome <> 'PROCESSED'`); n != 0 {
		t.Fatalf("%d messages were not processed fresh; want every one", n)
	}
	if n := c.transactions(); n != 48 {
		t.Fatalf("%d transactions; want 48", n)
	}
	for i, w := range wallets {
		if n := c.debits(w); n != 6 {
			t.Fatalf("wallet %d has %d debits; want 6", i, n)
		}
		c.reconcile(procs[i%len(procs)], w, "70.00")
	}
	c.requireQueuesDrained()
}

func TestHTTPAndSQSForTheSameOperation(t *testing.T) {
	c := newCluster(t, nil)
	a, b, _ := c.start("a", nil), c.start("b", nil), c.start("c", nil)
	w := c.open(a, "100.00")

	first := op{extID: "bet-1", kind: "BET", amount: "10.00"}
	viaHTTP := c.submit(a, w, first, http.StatusOK)
	c.send(w, "msg-1", "dedup-1", first)
	eventually(t, 20*time.Second, "msg-1", func() bool { outcome, _ := c.inbox("msg-1"); return outcome != "" })
	if outcome, id := c.inbox("msg-1"); outcome != "REPLAYED" || id != viaHTTP.str("transactionId") {
		t.Fatalf("msg-1 settled as %s on %s; want REPLAYED on the HTTP transaction %s", outcome, id, viaHTTP.str("transactionId"))
	}

	second := op{extID: "bet-2", kind: "BET", amount: "20.00"}
	var (
		racing response
		err    error
		wg     sync.WaitGroup
	)
	wg.Go(func() { racing, err = c.try(b, w, second) })
	c.send(w, "msg-2", "dedup-2", second)
	wg.Wait()
	if err != nil || racing.status != http.StatusOK {
		t.Fatalf("concurrent HTTP bet = %v %d %s; want 200", err, racing.status, racing.raw)
	}
	eventually(t, 20*time.Second, "msg-2", func() bool { outcome, _ := c.inbox("msg-2"); return outcome != "" })
	outcome, id := c.inbox("msg-2")
	if id != racing.str("transactionId") {
		t.Fatalf("msg-2 settled on %s; HTTP answered with %s", id, racing.str("transactionId"))
	}
	if httpFresh, sqsFresh := racing.body["idempotentReplay"] == false, outcome == "PROCESSED"; httpFresh == sqsFresh {
		t.Fatalf("HTTP fresh %t, message %s; want exactly one of them to apply the bet", httpFresh, outcome)
	}
	if n, d := c.transactions(), c.debits(w); n != 2 || d != 2 {
		t.Fatalf("%d transactions and %d debits; want two of each", n, d)
	}
	c.requireQueuesDrained()
	c.reconcile(b, w, "70.00")
}
