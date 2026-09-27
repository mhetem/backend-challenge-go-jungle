package outbox_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/workers/outbox"
)

var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

type release struct {
	next      time.Time
	lastError string
}

type fakeOutbox struct {
	mu       sync.Mutex
	claims   [][]app.OutboxMessage
	claimed  int
	markOK   bool
	marked   []uuid.UUID
	released map[uuid.UUID]release
	pending  int64
	oldest   time.Time
	midDrain int
}

func (f *fakeOutbox) Insert(context.Context, ...events.Event) error { return nil }

func (f *fakeOutbox) Claim(_ context.Context, _ string, _, _ time.Time, _ int) ([]app.OutboxMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimed++
	if len(f.claims) == 0 {
		return nil, nil
	}
	next := f.claims[0]
	f.claims = f.claims[1:]
	return next, nil
}

func (f *fakeOutbox) MarkPublished(_ context.Context, id uuid.UUID, _ string, _ time.Time) (bool, error) {
	f.marked = append(f.marked, id)
	return f.markOK, nil
}

func (f *fakeOutbox) Release(_ context.Context, id uuid.UUID, _ string, next time.Time, lastError string) (bool, error) {
	f.released[id] = release{next: next, lastError: lastError}
	return true, nil
}

func (f *fakeOutbox) Backlog(context.Context) (int64, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.claims) > 0 {
		f.midDrain++
	}
	return f.pending, f.oldest, nil
}

type store struct {
	app.Store
	outbox *fakeOutbox
}

func (s store) Outbox() app.Outbox { return s.outbox }

type runner struct{ s store }

func (r runner) InTx(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return fn(ctx, r.s)
}

func (r runner) InReadOnlySnapshot(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return fn(ctx, r.s)
}

type sender struct {
	fail  map[uuid.UUID]error
	sent  int
	delay time.Duration
}

func (s *sender) PublishEvents(_ context.Context, msgs []app.OutboxMessage) []error {
	time.Sleep(s.delay)
	s.sent += len(msgs)
	errs := make([]error, len(msgs))
	for i, m := range msgs {
		errs[i] = s.fail[m.ID]
	}
	return errs
}

func messages(n, attempts int) []app.OutboxMessage {
	out := make([]app.OutboxMessage, n)
	for i := range out {
		out[i] = app.OutboxMessage{ID: uuid.New(), Seq: int64(i + 1), PartitionKey: uuid.New(), EventType: "WalletBalanceChanged", Attempts: attempts}
	}
	return out
}

func newPublisher(t *testing.T, f *fakeOutbox, s *sender, batch int) (*outbox.Publisher, *prometheus.Registry) {
	t.Helper()
	f.released = map[uuid.UUID]release{}
	reg := prometheus.NewRegistry()
	cfg := config.OutboxConfig{PollInterval: time.Millisecond, BatchSize: batch, Lease: 30 * time.Second, BackoffBase: time.Second, BackoffCap: 5 * time.Minute}
	p, err := outbox.New(cfg, "test-owner", runner{store{outbox: f}}, s, func() time.Time { return t0 }, reg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return p, reg
}

func result(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "wallet_outbox_publish_results_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			if m.GetLabel()[0].GetValue() == name {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestPublishedEventsAreMarked(t *testing.T) {
	msgs := messages(13, 2)
	f := &fakeOutbox{claims: [][]app.OutboxMessage{msgs}, markOK: true}
	s := &sender{}
	p, reg := newPublisher(t, f, s, 50)
	if n := p.Tick(t.Context()); n != 13 || s.sent != 13 || len(f.marked) != 13 || len(f.released) != 0 {
		t.Fatalf("tick claimed %d, sent %d, marked %d, released %d; want 13 sent and marked", n, s.sent, len(f.marked), len(f.released))
	}
	if got := result(t, reg, "published"); got != 13 {
		t.Fatalf("published = %v; want 13", got)
	}
	if n, err := testutil.GatherAndCount(reg, "wallet_outbox_publish_attempts"); err != nil || n != 1 {
		t.Fatalf("attempts histogram series = %d, %v", n, err)
	}
}

func TestFailedEventsBackOffUpToTheCap(t *testing.T) {
	fresh, stuck := messages(1, 1)[0], messages(1, 30)[0]
	down := errors.New("dial tcp: connection refused")
	f := &fakeOutbox{claims: [][]app.OutboxMessage{{fresh, stuck}}, markOK: true}
	s := &sender{fail: map[uuid.UUID]error{fresh.ID: down, stuck.ID: down}}
	p, reg := newPublisher(t, f, s, 50)
	p.Tick(t.Context())
	first, capped := f.released[fresh.ID], f.released[stuck.ID]
	if wait := first.next.Sub(t0); wait < time.Second || wait > 1200*time.Millisecond || first.lastError != down.Error() {
		t.Fatalf("first failure retries in %s with %q; want 1s plus at most 20%% jitter", wait, first.lastError)
	}
	if wait := capped.next.Sub(t0); wait != 5*time.Minute {
		t.Fatalf("thirtieth failure retries in %s; want exactly the 5m cap", wait)
	}
	if got := result(t, reg, "failed"); got != 2 || len(f.marked) != 0 {
		t.Fatalf("failed = %v, marked %d; want 2 failed and nothing marked", got, len(f.marked))
	}
}

func TestLostClaimsAreCounted(t *testing.T) {
	f := &fakeOutbox{claims: [][]app.OutboxMessage{messages(3, 1)}, markOK: false}
	p, reg := newPublisher(t, f, &sender{}, 50)
	p.Tick(t.Context())
	if got, published := result(t, reg, "lost"), result(t, reg, "published"); got != 3 || published != 0 {
		t.Fatalf("lost = %v, published = %v; want 3 lost to the publisher that took over", got, published)
	}
}

func TestRunDrainsFullBatchesAndObservesTheBacklog(t *testing.T) {
	f := &fakeOutbox{
		claims:  [][]app.OutboxMessage{messages(5, 1), messages(5, 1), messages(2, 1)},
		markOK:  true,
		pending: 7,
		oldest:  t0.Add(-90 * time.Second),
	}
	p, reg := newPublisher(t, f, &sender{}, 5)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		f.mu.Lock()
		claimed := f.claimed
		f.mu.Unlock()
		if claimed >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if got := result(t, reg, "published"); got != 12 {
		t.Fatalf("published = %v; want all 12 across the drained batches", got)
	}
	pending, err := testutil.GatherAndCount(reg, "wallet_outbox_pending")
	if err != nil || pending != 1 {
		t.Fatalf("pending gauge series = %d, %v", pending, err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range families {
		switch fam.GetName() {
		case "wallet_outbox_pending":
			if v := fam.GetMetric()[0].GetGauge().GetValue(); v != 7 {
				t.Fatalf("pending = %v; want 7", v)
			}
		case "wallet_outbox_oldest_pending_age_seconds":
			if v := fam.GetMetric()[0].GetGauge().GetValue(); v != 90 {
				t.Fatalf("oldest pending age = %v; want 90", v)
			}
		}
	}
}

func TestBacklogIsObservedWhileDraining(t *testing.T) {
	var claims [][]app.OutboxMessage
	for range 40 {
		claims = append(claims, messages(2, 1))
	}
	f := &fakeOutbox{claims: append(claims, messages(1, 1)), markOK: true, pending: 81, oldest: t0.Add(-time.Minute)}
	p, _ := newPublisher(t, f, &sender{delay: time.Millisecond}, 2)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		left := len(f.claims)
		f.mu.Unlock()
		if left == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.midDrain < 2 {
		t.Fatalf("backlog observed %d times while full batches were still draining; want it refreshed every 10 poll intervals", f.midDrain)
	}
}

func TestPublishLinksEachEventToItsOrigin(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
	msgs := messages(2, 1)
	msgs[0].TraceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	f := &fakeOutbox{claims: [][]app.OutboxMessage{msgs}, markOK: true}
	p, _ := newPublisher(t, f, &sender{}, 50)
	p.Tick(t.Context())

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("%d spans; want one per published batch", len(ended))
	}
	span := ended[0]
	links := span.Links()
	if span.Name() != "publish events" || span.SpanKind() != trace.SpanKindProducer || len(links) != 1 ||
		links[0].SpanContext.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || links[0].SpanContext.SpanID().String() != "00f067aa0ba902b7" {
		t.Fatalf("span %q (%v) with links %+v; want a producer span linked to the one traced origin", span.Name(), span.SpanKind(), links)
	}
}
