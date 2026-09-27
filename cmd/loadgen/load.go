package main

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const historySize = 256

type operation struct {
	extID  string
	kind   string
	wallet wallet
	round  string
	minor  int64
	ref    string
}

func (o operation) body() map[string]any {
	b := map[string]any{
		"providerId":            providerClient,
		"externalTransactionId": o.extID,
		"playerId":              o.wallet.player,
		"walletId":              o.wallet.id,
		"roundId":               o.round,
		"gameId":                "loadgen",
		"kind":                  o.kind,
		"money":                 map[string]string{"amount": amount(o.minor), "currency": "BRL"},
	}
	if o.ref != "" {
		b["referenceExternalTransactionId"] = o.ref
	}
	return b
}

func amount(minor int64) string {
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

type sample struct {
	label   string
	outcome string
	latency time.Duration
}

type counters struct {
	requests atomic.Int64
	errors   atomic.Int64
}

type worker struct {
	id      int
	run     string
	o       options
	api     *api
	wallets []wallet
	rng     *rand.Rand
	seq     int
	calls   int
	history []operation
	bets    []operation
	wins    []operation
	samples []sample
}

func (w *worker) plan() (operation, bool) {
	if len(w.history) > 0 && w.rng.Float64() < w.o.replays {
		return w.history[w.rng.IntN(len(w.history))], true
	}
	w.seq++
	op := operation{extID: fmt.Sprintf("lg-%s-%d-%d", w.run, w.id, w.seq)}
	op.round = "round-" + op.extID
	pick := w.rng.IntN(100)
	switch {
	case pick >= 95 && len(w.bets)+len(w.wins) > 0:
		ref := w.takeReversible()
		op.kind, op.ref, op.wallet, op.round, op.minor = "ROLLBACK", ref.extID, ref.wallet, ref.round, ref.minor
	case pick >= 90 && pick < 95 && len(w.bets) > 0:
		ref := take(w.rng, &w.bets)
		op.kind, op.ref, op.wallet, op.round, op.minor = "REFUND", ref.extID, ref.wallet, ref.round, ref.minor
	case pick >= 85 && pick < 90:
		op.kind, op.wallet = "LOSS", w.pickWallet()
	case pick >= 60 && pick < 85:
		op.kind, op.wallet, op.minor = "WIN", w.pickWallet(), w.rng.Int64N(5000)+1
	default:
		op.kind, op.wallet, op.minor = "BET", w.pickWallet(), w.rng.Int64N(5000)+1
	}
	return op, false
}

func (w *worker) pickWallet() wallet {
	if w.o.hot > 0 && w.rng.Float64() < w.o.hot {
		return w.wallets[0]
	}
	return w.wallets[w.rng.IntN(len(w.wallets))]
}

func (w *worker) takeReversible() operation {
	if len(w.wins) == 0 || (len(w.bets) > 0 && w.rng.IntN(2) == 0) {
		return take(w.rng, &w.bets)
	}
	return take(w.rng, &w.wins)
}

func take(rng *rand.Rand, pool *[]operation) operation {
	i, last := rng.IntN(len(*pool)), len(*pool)-1
	op := (*pool)[i]
	(*pool)[i] = (*pool)[last]
	*pool = (*pool)[:last]
	return op
}

func (w *worker) remember(op operation, outcome string) {
	if outcome == "processed" {
		switch op.kind {
		case "BET":
			w.bets = append(w.bets, op)
		case "WIN":
			w.wins = append(w.wins, op)
		}
	}
	switch {
	case failed(outcome):
	case len(w.history) < historySize:
		w.history = append(w.history, op)
	default:
		w.history[w.seq%historySize] = op
	}
}

func (w *worker) fire(at time.Time, c *counters) {
	op, replay := w.plan()
	target := w.o.targets[w.calls%len(w.o.targets)]
	w.calls++
	r, err := w.api.submit(context.Background(), target, op)
	s := sample{label: op.kind, outcome: classify(r, err), latency: time.Since(at)}
	if replay {
		s.label = "REPLAY"
	}
	w.samples = append(w.samples, s)
	c.requests.Add(1)
	if failed(s.outcome) {
		c.errors.Add(1)
	}
	if !replay {
		w.remember(op, s.outcome)
	}
}

func classify(r reply, err error) string {
	switch {
	case err != nil:
		return "transport_error"
	case r.replay:
		return "replayed"
	case r.status == http.StatusOK:
		return "processed"
	case r.status == http.StatusAccepted:
		return "pending_reference"
	case r.status == http.StatusUnprocessableEntity:
		return "rejected:" + r.failure
	case r.status == http.StatusConflict:
		return "conflict:" + r.code
	case r.status == http.StatusServiceUnavailable:
		return "unavailable"
	}
	return "http_" + strconv.Itoa(r.status)
}

func failed(outcome string) bool {
	return outcome == "transport_error" || outcome == "unavailable" || strings.HasPrefix(outcome, "http_")
}

type result struct {
	start   time.Time
	elapsed time.Duration
	samples []sample
}

func generate(ctx context.Context, o options, a *api, wallets []wallet, run string, progress io.Writer) result {
	start := time.Now()
	end := start.Add(o.duration)
	var c counters
	workers := make([]*worker, o.concurrency)
	for i := range workers {
		workers[i] = &worker{id: i, run: run, o: o, api: a, wallets: wallets, calls: i,
			rng: rand.New(rand.NewPCG(uint64(start.UnixNano()), uint64(i)))}
	}
	var ticks chan time.Time
	var wg sync.WaitGroup
	if o.rate > 0 {
		ticks = make(chan time.Time, o.concurrency)
		wg.Go(func() { dispatch(ctx, ticks, start, end, o.rate) })
	}
	for _, w := range workers {
		wg.Go(func() {
			if ticks != nil {
				for at := range ticks {
					w.fire(at, &c)
				}
				return
			}
			for ctx.Err() == nil && time.Now().Before(end) {
				w.fire(time.Now(), &c)
			}
		})
	}
	stop := watch(progress, &c, start)
	wg.Wait()
	stop()
	res := result{start: start, elapsed: time.Since(start)}
	for _, w := range workers {
		res.samples = append(res.samples, w.samples...)
	}
	return res
}

func dispatch(ctx context.Context, ticks chan<- time.Time, start, end time.Time, rate int) {
	defer close(ticks)
	interval := time.Second / time.Duration(rate)
	for at := start; at.Before(end); at = at.Add(interval) {
		if wait := time.Until(at); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		select {
		case <-ctx.Done():
			return
		case ticks <- at:
		}
	}
}

func watch(progress io.Writer, c *counters, start time.Time) func() {
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				fmt.Fprintf(progress, "loadgen: %s: %d requests, %d errors\n", time.Since(start).Round(time.Second), c.requests.Load(), c.errors.Load())
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
