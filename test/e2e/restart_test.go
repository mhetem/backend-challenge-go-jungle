//go:build e2e

package e2e_test

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRestartingEveryInstance(t *testing.T) {
	c := newCluster(t, nil)
	first := []*process{c.start("a", nil), c.start("b", nil), c.start("c", nil)}
	w1, w2 := c.open(first[0], "100.00"), c.open(first[1], "50.00")
	steps := []struct {
		w      wallet
		o      op
		status int
	}{
		{w1, op{extID: "bet-1", kind: "BET", amount: "30.00"}, http.StatusOK},
		{w1, op{extID: "win-1", kind: "WIN", amount: "10.00"}, http.StatusOK},
		{w2, op{extID: "bet-2", kind: "BET", amount: "20.00"}, http.StatusOK},
		{w2, op{extID: "rollback-3", kind: "ROLLBACK", amount: "5.00", ref: "bet-3"}, http.StatusAccepted},
	}
	before := make([]response, len(steps))
	for i, s := range steps {
		before[i] = c.submit(first[i%len(first)], s.w, s.o, s.status)
	}
	viaSQS := op{extID: "bet-4", kind: "BET", amount: "10.00"}
	c.send(w1, "msg-1", "dedup-1", viaSQS)
	eventually(t, 20*time.Second, "msg-1", func() bool { outcome, _ := c.inbox("msg-1"); return outcome == "PROCESSED" })
	c.requireQueuesDrained()

	c.kill(first[0])
	c.terminate(first[1], first[2])

	second := []*process{c.start("d", nil), c.start("e", nil), c.start("f", nil)}
	for i, s := range steps {
		r := c.submit(second[i%len(second)], s.w, s.o, s.status)
		if r.body["idempotentReplay"] != true || r.str("transactionId") != before[i].str("transactionId") ||
			r.str("status") != before[i].str("status") || r.money("balance") != before[i].money("balance") {
			t.Fatalf("%s after the restart = %s; want a replay of %s", s.o.extID, r.raw, before[i].raw)
		}
	}
	c.send(w1, "msg-1", "dedup-2", viaSQS)
	eventually(t, 20*time.Second, "the resent message to be recognised", func() bool { return c.sum(consumed("duplicate"), second...) == 1 })

	c.submit(second[0], w2, op{extID: "bet-3", kind: "BET", amount: "5.00"}, http.StatusOK)
	eventually(t, 15*time.Second, "the pending rollback to settle", func() bool {
		return c.transaction(second[1], "rollback-3").str("status") == "PROCESSED"
	})

	c.requireQueuesDrained()
	if n := c.transactions(); n != 6 {
		t.Fatalf("%d transactions; want 6", n)
	}
	c.reconcile(second[0], w1, "70.00")
	c.reconcile(second[1], w2, "30.00")
}

type targets struct {
	mu    sync.Mutex
	procs []*process
	next  int
}

func (ts *targets) pick() *process {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	p := ts.procs[ts.next%len(ts.procs)]
	ts.next++
	return p
}

func (ts *targets) set(ps ...*process) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.procs = ps
}

func (c *cluster) deliver(ts *targets, w wallet, o op) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		r, err := c.try(ts.pick(), w, o)
		switch {
		case err == nil && r.status == http.StatusOK:
			return nil
		case err == nil && r.status != http.StatusServiceUnavailable:
			return fmt.Errorf("%s: %d %s", o.extID, r.status, r.raw)
		case time.Now().After(deadline):
			return fmt.Errorf("%s: no instance took it within 60s (last error %v)", o.extID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSIGTERMUnderLoad(t *testing.T) {
	c := newCluster(t, nil)
	a, b, other := c.start("a", nil), c.start("b", nil), c.start("c", nil)
	live := &targets{procs: []*process{a, b, other}}
	wallets := make([]wallet, 4)
	for i := range wallets {
		wallets[i] = c.open(a, "100.00")
	}

	type job struct {
		w wallet
		o op
	}
	jobs := make(chan job)
	errs := make(chan error, 60*len(wallets))
	var delivered atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for j := range jobs {
				if err := c.deliver(live, j.w, j.o); err != nil {
					errs <- err
				}
				delivered.Add(1)
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
	wg.Go(func() {
		defer close(jobs)
		for n := range 60 {
			for i, w := range wallets {
				jobs <- job{w, op{extID: fmt.Sprintf("http-%d-%d", i, n), kind: "BET", amount: "1.00"}}
			}
		}
	})
	enqueue := func(round int) {
		for i, w := range wallets {
			for n := range 5 {
				id := fmt.Sprintf("%d-%d-%d", round, i, n)
				c.send(w, "msg-"+id, "dedup-"+id, op{extID: "sqs-" + id, kind: "BET", amount: "1.00"})
			}
		}
	}

	enqueue(0)
	eventually(t, 60*time.Second, "a third of the HTTP load", func() bool { return delivered.Load() >= 80 })
	b.signal(syscall.SIGTERM)
	live.set(a, other)
	enqueue(1)
	eventually(t, 60*time.Second, "two thirds of the HTTP load", func() bool { return delivered.Load() >= 160 })
	other.signal(syscall.SIGTERM)
	live.set(a)
	enqueue(2)
	c.expectExit(b, 0, 30*time.Second)
	c.expectExit(other, 0, 30*time.Second)

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}

	eventually(t, 60*time.Second, "all 60 messages", func() bool { return c.count(`SELECT count(*) FROM inbox_messages`) == 60 })
	c.requireQueuesDrained()
	if n := c.transactions(); n != 300 {
		t.Fatalf("%d transactions; want 300, one per operation", n)
	}
	for i, w := range wallets {
		if n := c.debits(w); n != 75 {
			t.Fatalf("wallet %d has %d debits; want 75", i, n)
		}
		c.reconcile(a, w, "25.00")
	}
}
