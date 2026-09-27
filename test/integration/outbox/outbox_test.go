//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres"
	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/sqs"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/workers/outbox"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
)

var (
	t0      = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	discard = slog.New(slog.DiscardHandler)
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type noMetrics struct{}

func (noMetrics) ReconciliationDiverged() {}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, overrides map[string]string) config.Config {
	t.Helper()
	cfg, err := config.Parse(func(key string) (string, bool) {
		if v, ok := overrides[key]; ok {
			return v, true
		}
		return os.LookupEnv(key)
	})
	must(t, err)
	return cfg
}

type harness struct {
	t        *testing.T
	db       *dbtest.Database
	clock    *clock
	queue    string
	queueURL string
	control  *sqs.Client
}

func setup(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, db: dbtest.New(t), clock: &clock{now: t0}, queue: "events-" + uuid.NewString()[:8] + ".fifo"}
	h.control = h.client("")
	out, err := h.control.API.CreateQueue(t.Context(), &awssqs.CreateQueueInput{
		QueueName:  aws.String(h.queue),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"},
	})
	must(t, err)
	h.queueURL = aws.ToString(out.QueueUrl)
	t.Cleanup(func() {
		_, _ = h.control.API.DeleteQueue(context.Background(), &awssqs.DeleteQueueInput{QueueUrl: aws.String(h.queueURL)})
	})
	return h
}

func (h *harness) client(endpoint string) *sqs.Client {
	h.t.Helper()
	overrides := map[string]string{}
	if endpoint != "" {
		overrides["AWS_ENDPOINT_URL"] = endpoint
		overrides["SQS_EVENTS_QUEUE"] = h.queue
	}
	c, err := sqs.New(load(h.t, overrides), discard)
	must(h.t, err)
	must(h.t, c.Start(h.t.Context()))
	h.t.Cleanup(func() { _ = c.Stop(context.Background()) })
	return c
}

func (h *harness) runner() app.TxRunner {
	h.t.Helper()
	cfg := postgres.Config{URL: h.db.AppURL, ApplicationName: "outbox-test", MaxConns: 8,
		LockTimeout: 5 * time.Second, StatementTimeout: 10 * time.Second, TxAttempts: 3, TxBackoff: 5 * time.Millisecond}
	pool, err := postgres.NewPool(context.Background(), cfg)
	must(h.t, err)
	h.t.Cleanup(pool.Close)
	return postgres.NewTxRunner(pool, cfg)
}

func (h *harness) publisher(owner string, tx app.TxRunner, sender outbox.Sender, batch int) (*outbox.Publisher, *prometheus.Registry) {
	h.t.Helper()
	reg := prometheus.NewRegistry()
	p, err := outbox.New(config.OutboxConfig{PollInterval: 10 * time.Millisecond, BatchSize: batch, Lease: 30 * time.Second,
		BackoffBase: time.Second, BackoffCap: 5 * time.Minute}, owner, tx, sender, h.clock.Now, reg, discard)
	must(h.t, err)
	return p, reg
}

func (h *harness) seed(tx app.TxRunner, wallets int) []uuid.UUID {
	h.t.Helper()
	svc := app.NewWalletService(tx, h.clock.Now, app.NewID, discard, noMetrics{})
	var ids []uuid.UUID
	for range wallets {
		cmd, err := app.OpenWalletRequest{
			PlayerID:       uuid.NewString(),
			InitialBalance: app.MoneyRequest{Amount: "100.00", Currency: "BRL"},
		}.Command("corr-seed")
		must(h.t, err)
		w, err := svc.Open(h.t.Context(), cmd)
		must(h.t, err)
		ids = append(ids, w.ID)
	}
	return ids
}

type row struct {
	id          uuid.UUID
	partition   uuid.UUID
	eventType   string
	attempts    int
	claimedBy   *string
	published   bool
	lastError   *string
	nextAttempt time.Time
}

func (h *harness) rows() []row {
	h.t.Helper()
	rows, err := h.db.App.Query(h.t.Context(), `SELECT id, partition_key, event_type, attempts, claimed_by,
		published_at IS NOT NULL, last_error, next_attempt_at FROM outbox_events ORDER BY seq`)
	must(h.t, err)
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		must(h.t, rows.Scan(&r.id, &r.partition, &r.eventType, &r.attempts, &r.claimedBy, &r.published, &r.lastError, &r.nextAttempt))
		out = append(out, r)
	}
	must(h.t, rows.Err())
	return out
}

type message struct {
	groupID     string
	dedupID     string
	eventType   string
	eventID     string
	traceParent string
}

func (h *harness) receive(want int, wait time.Duration) []message {
	h.t.Helper()
	var got []message
	deadline := time.Now().Add(wait)
	for len(got) < want && time.Now().Before(deadline) {
		out, err := h.control.API.ReceiveMessage(h.t.Context(), &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(h.queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameMessageGroupId, types.MessageSystemAttributeNameMessageDeduplicationId,
			},
			MessageAttributeNames: []string{"All"},
		})
		must(h.t, err)
		for _, m := range out.Messages {
			var body struct {
				EventID string `json:"eventId"`
			}
			must(h.t, json.Unmarshal([]byte(aws.ToString(m.Body)), &body))
			got = append(got, message{
				groupID:     m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
				dedupID:     m.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)],
				eventType:   aws.ToString(m.MessageAttributes["eventType"].StringValue),
				eventID:     body.EventID,
				traceParent: aws.ToString(m.MessageAttributes["traceparent"].StringValue),
			})
			_, err := h.control.API.DeleteMessage(h.t.Context(), &awssqs.DeleteMessageInput{QueueUrl: aws.String(h.queueURL), ReceiptHandle: m.ReceiptHandle})
			must(h.t, err)
		}
	}
	return got
}

func (h *harness) requireDelivered(rows []row) {
	h.t.Helper()
	got := h.receive(len(rows), 15*time.Second)
	if extra := h.receive(1, 2*time.Second); len(extra) != 0 {
		h.t.Fatalf("an event was delivered twice: %+v", extra)
	}
	if len(got) != len(rows) {
		h.t.Fatalf("received %d messages; want %d", len(got), len(rows))
	}
	for _, r := range rows {
		i := slices.IndexFunc(got, func(m message) bool { return m.dedupID == r.id.String() })
		if i < 0 {
			h.t.Fatalf("event %s never arrived", r.id)
		}
		if m := got[i]; m.eventID != r.id.String() || m.groupID != r.partition.String() || m.eventType != r.eventType {
			h.t.Fatalf("message %+v; want eventId and dedup id %s, group %s, type %s", m, r.id, r.partition, r.eventType)
		}
	}
}

func count(reg *prometheus.Registry, result string) int {
	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() != "wallet_outbox_publish_results_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			if m.GetLabel()[0].GetValue() == result {
				return int(m.GetCounter().GetValue())
			}
		}
	}
	return 0
}

func TestUnreachableSQSBacksOffAndRecovers(t *testing.T) {
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	h := setup(t)
	upstream, err := url.Parse(os.Getenv("AWS_ENDPOINT_URL"))
	must(t, err)
	var down atomic.Bool
	forward := httputil.NewSingleHostReverseProxy(upstream)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)

	tx := h.runner()
	h.seed(tx, 1)
	p, reg := h.publisher("recovering", tx, h.client(proxy.URL), 50)

	down.Store(true)
	start := t0.Add(time.Minute)
	h.clock.Set(start)
	if n := p.Tick(t.Context()); n != 2 {
		t.Fatalf("claimed %d; want both events of the opening", n)
	}
	for _, r := range h.rows() {
		wait := r.nextAttempt.Sub(start)
		if r.published || r.attempts != 1 || r.claimedBy != nil || r.lastError == nil || *r.lastError == "" ||
			wait < time.Second || wait > 1200*time.Millisecond {
			t.Fatalf("after the first failure: %+v (retry in %s); want released, attempts 1, retry in about 1s", r, wait)
		}
	}
	if n := p.Tick(t.Context()); n != 0 {
		t.Fatalf("claimed %d before the backoff elapsed", n)
	}

	second := start.Add(2 * time.Second)
	h.clock.Set(second)
	p.Tick(t.Context())
	for _, r := range h.rows() {
		if wait := r.nextAttempt.Sub(second); r.attempts != 2 || wait < 2*time.Second || wait > 2400*time.Millisecond {
			t.Fatalf("after the second failure: %+v (retry in %s); want attempts 2, retry in about 2s", r, wait)
		}
	}

	down.Store(false)
	h.clock.Set(second.Add(5 * time.Second))
	if n := p.Tick(t.Context()); n != 2 {
		t.Fatalf("claimed %d once SQS was back; want 2", n)
	}
	rows := h.rows()
	for _, r := range rows {
		if !r.published || r.attempts != 3 || r.lastError != nil {
			t.Fatalf("after recovery: %+v; want published on the third attempt with the error cleared", r)
		}
	}
	if failed, published := count(reg, "failed"), count(reg, "published"); failed != 4 || published != 2 {
		t.Fatalf("results: %d failed, %d published; want 4 and 2", failed, published)
	}
	h.requireDelivered(rows)
}

func TestTwoPublishersPublishEveryEventOnce(t *testing.T) {
	t.Parallel()
	h := setup(t)
	h.clock.Set(t0.Add(time.Minute))
	h.seed(h.runner(), 10)
	sender := h.client(os.Getenv("AWS_ENDPOINT_URL"))
	a, regA := h.publisher("publisher-a", h.runner(), sender, 3)
	b, regB := h.publisher("publisher-b", h.runner(), sender, 3)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, p := range []*outbox.Publisher{a, b} {
		wg.Go(func() {
			<-start
			for p.Tick(context.Background()) > 0 {
			}
		})
	}
	close(start)
	wg.Wait()

	rows := h.rows()
	if len(rows) != 20 {
		t.Fatalf("%d outbox rows; want 20", len(rows))
	}
	for _, r := range rows {
		if !r.published || r.attempts != 1 || r.claimedBy == nil || (*r.claimedBy != "publisher-a" && *r.claimedBy != "publisher-b") {
			t.Fatalf("row %+v; want published once by one of the two publishers", r)
		}
	}
	if total := count(regA, "published") + count(regB, "published"); total != 20 || count(regA, "lost")+count(regB, "lost") != 0 {
		t.Fatalf("published %d across both publishers, lost %d; want 20 and 0", total, count(regA, "lost")+count(regB, "lost"))
	}
	h.requireDelivered(rows)
}

func TestExpiredLeaseIsTakenOver(t *testing.T) {
	t.Parallel()
	h := setup(t)
	tx := h.runner()
	h.seed(tx, 1)
	now := t0.Add(time.Minute)
	h.clock.Set(now)
	var abandoned []app.OutboxMessage
	must(t, tx.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		var err error
		abandoned, err = s.Outbox().Claim(ctx, "crashed", now, now.Add(30*time.Second), 10)
		return err
	}))
	if len(abandoned) != 2 {
		t.Fatalf("the crashed publisher claimed %d; want 2", len(abandoned))
	}

	p, reg := h.publisher("survivor", tx, h.client(os.Getenv("AWS_ENDPOINT_URL")), 50)
	if n := p.Tick(t.Context()); n != 0 {
		t.Fatalf("took over %d events while the lease was still valid", n)
	}
	h.clock.Set(now.Add(31 * time.Second))
	if n := p.Tick(t.Context()); n != 2 || count(reg, "published") != 2 {
		t.Fatalf("took over %d events after the lease expired; want both published", n)
	}
	rows := h.rows()
	for _, r := range rows {
		if !r.published || r.attempts != 2 || *r.claimedBy != "survivor" {
			t.Fatalf("row %+v; want published by the survivor on the second claim", r)
		}
	}
	var late bool
	must(t, tx.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		var err error
		late, err = s.Outbox().MarkPublished(ctx, abandoned[0].ID, "crashed", now.Add(time.Minute))
		return err
	}))
	if late {
		t.Fatal("the crashed publisher's late mark was accepted")
	}
	h.requireDelivered(rows)
}

func TestRepublishAfterACrashKeepsTheEventID(t *testing.T) {
	t.Parallel()
	h := setup(t)
	tx := h.runner()
	h.seed(tx, 1)
	sender := h.client(os.Getenv("AWS_ENDPOINT_URL"))
	now := t0.Add(time.Minute)
	h.clock.Set(now)
	var sent []app.OutboxMessage
	must(t, tx.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		var err error
		sent, err = s.Outbox().Claim(ctx, "crashed", now, now.Add(30*time.Second), 10)
		return err
	}))
	for i, err := range sender.PublishEvents(t.Context(), sent) {
		if err != nil {
			t.Fatalf("first publish of %s: %v", sent[i].ID, err)
		}
	}

	h.clock.Set(now.Add(31 * time.Second))
	p, reg := h.publisher("survivor", tx, sender, 50)
	if n := p.Tick(t.Context()); n != 2 || count(reg, "published") != 2 {
		t.Fatalf("republished %d; want both events again", n)
	}
	rows := h.rows()
	for _, r := range rows {
		if !r.published || r.attempts != 2 {
			t.Fatalf("row %+v; want published on the second claim", r)
		}
	}
	h.requireDelivered(rows)
}

func TestEventsCarryTheTraceOfTheOperationThatCreatedThem(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	h := setup(t)
	tx := h.runner()
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	must(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	must(t, err)
	ctx := trace.ContextWithRemoteSpanContext(t.Context(),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true}))
	const want = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

	cmd, err := app.OpenWalletRequest{
		PlayerID:       uuid.NewString(),
		InitialBalance: app.MoneyRequest{Amount: "100.00", Currency: "BRL"},
	}.Command("corr-traced")
	must(t, err)
	_, err = app.NewWalletService(tx, h.clock.Now, app.NewID, discard, noMetrics{}).Open(ctx, cmd)
	must(t, err)
	var stored []string
	rows, err := h.db.App.Query(t.Context(), `SELECT coalesce(trace_parent, '') FROM outbox_events ORDER BY seq`)
	must(t, err)
	for rows.Next() {
		var tp string
		must(t, rows.Scan(&tp))
		stored = append(stored, tp)
	}
	must(t, rows.Err())
	if len(stored) != 2 || stored[0] != want || stored[1] != want {
		t.Fatalf("stored trace parents %q; want %s on both opening events", stored, want)
	}

	h.clock.Set(t0.Add(time.Minute))
	p, _ := h.publisher("traced", tx, h.client(os.Getenv("AWS_ENDPOINT_URL")), 50)
	if n := p.Tick(t.Context()); n != 2 {
		t.Fatalf("published %d; want 2", n)
	}
	got := h.receive(2, 15*time.Second)
	if len(got) != 2 || got[0].traceParent != want || got[1].traceParent != want {
		t.Fatalf("messages %+v; want both carrying traceparent %s", got, want)
	}
}
