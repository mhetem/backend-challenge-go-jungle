package resolver_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/workers/resolver"
)

type step struct {
	res app.Resolution
	err error
}

type fakeService struct {
	mu       sync.Mutex
	due      []app.DueTransaction
	dueErr   error
	scripts  map[uuid.UUID][]step
	resolved map[uuid.UUID]int
	failed   []uuid.UUID
	ticks    int
}

func (f *fakeService) Due(context.Context, int) ([]app.DueTransaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ticks++
	return f.due, f.dueErr
}

func (f *fakeService) Resolve(_ context.Context, d app.DueTransaction) (app.Resolution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved[d.ID]++
	script := f.scripts[d.ID]
	if len(script) == 0 {
		return app.Resolution{Status: wager.Processed}, nil
	}
	f.scripts[d.ID] = script[1:]
	return script[0].res, script[0].err
}

func (f *fakeService) Fail(_ context.Context, d app.DueTransaction) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, d.ID)
	return true, nil
}

func newResolver(t *testing.T, f *fakeService) (*resolver.Resolver, *prometheus.Registry) {
	t.Helper()
	if f.scripts == nil {
		f.scripts = map[uuid.UUID][]step{}
	}
	f.resolved = map[uuid.UUID]int{}
	reg := prometheus.NewRegistry()
	r, err := resolver.New(config.ResolverConfig{PollInterval: 5 * time.Millisecond, BatchSize: 10, MaxFailures: 3}, f, reg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return r, reg
}

func requireOutcomes(t *testing.T, reg *prometheus.Registry, counts map[string]int) {
	t.Helper()
	var want strings.Builder
	want.WriteString("# HELP wallet_resolver_outcomes_total Pending-reference attempts by outcome: processed, rejected, rescheduled, skipped, failed or error.\n")
	want.WriteString("# TYPE wallet_resolver_outcomes_total counter\n")
	for _, name := range []string{"error", "failed", "processed", "rejected", "rescheduled", "skipped"} {
		if n, ok := counts[name]; ok {
			fmt.Fprintf(&want, "wallet_resolver_outcomes_total{outcome=%q} %d\n", name, n)
		}
	}
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want.String()), "wallet_resolver_outcomes_total"); err != nil {
		t.Fatal(err)
	}
}

func due(n int) []app.DueTransaction {
	out := make([]app.DueTransaction, n)
	for i := range out {
		out[i] = app.DueTransaction{ID: uuid.New(), WalletID: uuid.New()}
	}
	return out
}

func TestTickResolvesEveryDueTransaction(t *testing.T) {
	ds := due(4)
	f := &fakeService{due: ds, scripts: map[uuid.UUID][]step{
		ds[1].ID: {{res: app.Resolution{Status: wager.PendingReference}}},
		ds[2].ID: {{res: app.Resolution{Status: wager.Rejected}}},
		ds[3].ID: {{res: app.Resolution{Status: wager.Processed, Skipped: true}}},
	}}
	r, reg := newResolver(t, f)
	r.Tick(t.Context())
	for _, d := range ds {
		if f.resolved[d.ID] != 1 {
			t.Fatalf("%s resolved %d times; want once", d.ID, f.resolved[d.ID])
		}
	}
	requireOutcomes(t, reg, map[string]int{"processed": 1, "rescheduled": 1, "rejected": 1, "skipped": 1})
}

func TestRepeatedPermanentErrorsMarkTheTransactionFailed(t *testing.T) {
	ds := due(2)
	broken := errors.New("corrupt stored state")
	f := &fakeService{due: ds, scripts: map[uuid.UUID][]step{
		ds[0].ID: {{err: broken}, {err: broken}, {err: broken}},
		ds[1].ID: {{err: broken}, {err: broken}, {res: app.Resolution{Status: wager.PendingReference}}, {err: broken}, {err: broken}},
	}}
	r, reg := newResolver(t, f)
	for range 3 {
		r.Tick(t.Context())
	}
	if len(f.failed) != 1 || f.failed[0] != ds[0].ID {
		t.Fatalf("failed %v; want only %s, after its third consecutive error", f.failed, ds[0].ID)
	}
	for range 2 {
		r.Tick(t.Context())
	}
	if len(f.failed) != 1 {
		t.Fatalf("failed %v; the success in between must reset %s's count", f.failed, ds[1].ID)
	}
	requireOutcomes(t, reg, map[string]int{"error": 7, "failed": 1, "processed": 2, "rescheduled": 1})
}

func TestTransientErrorsNeverFailATransaction(t *testing.T) {
	ds := due(1)
	down := fmt.Errorf("%w: connection refused", app.ErrTransient)
	f := &fakeService{due: ds, scripts: map[uuid.UUID][]step{
		ds[0].ID: {{err: down}, {err: down}, {err: down}, {err: app.ErrConcurrentUpdate}, {err: down}},
	}}
	r, reg := newResolver(t, f)
	for range 5 {
		r.Tick(t.Context())
	}
	if len(f.failed) != 0 {
		t.Fatalf("transient errors marked %v FAILED", f.failed)
	}
	requireOutcomes(t, reg, map[string]int{"error": 5})
}

func TestFailureCountsForgetTransactionsNoLongerDue(t *testing.T) {
	ds := due(1)
	broken := errors.New("corrupt stored state")
	f := &fakeService{due: ds, scripts: map[uuid.UUID][]step{ds[0].ID: {{err: broken}, {err: broken}, {err: broken}}}}
	r, _ := newResolver(t, f)
	r.Tick(t.Context())
	r.Tick(t.Context())
	f.due = nil
	r.Tick(t.Context())
	f.due = ds
	r.Tick(t.Context())
	if len(f.failed) != 0 {
		t.Fatalf("a transaction another instance settled kept its count and was failed: %v", f.failed)
	}
}

func TestListingErrorsResolveNothing(t *testing.T) {
	f := &fakeService{due: due(2), dueErr: fmt.Errorf("%w: pool exhausted", app.ErrTransient)}
	r, _ := newResolver(t, f)
	r.Tick(t.Context())
	if len(f.resolved) != 0 {
		t.Fatalf("resolved %v after the listing failed", f.resolved)
	}
}

func TestRunTicksUntilCancelled(t *testing.T) {
	f := &fakeService{}
	r, _ := newResolver(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		f.mu.Lock()
		ticks := f.ticks
		f.mu.Unlock()
		if ticks >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ticks < 3 {
		t.Fatalf("%d ticks in a second at a 5ms interval", f.ticks)
	}
}
