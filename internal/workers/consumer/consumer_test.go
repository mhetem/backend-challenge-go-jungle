package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/sqs"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/workers/consumer"
)

const (
	playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	walletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
)

type deadLetter struct {
	group  string
	reason string
	detail string
}

type fakeQueue struct {
	mu         sync.Mutex
	batches    [][]sqs.Message
	received   int
	deleted    []string
	visibility map[string]time.Duration
	dead       map[string]deadLetter
	deadErr    error
}

func (q *fakeQueue) Receive(ctx context.Context, _, _ time.Duration) ([]sqs.Message, error) {
	q.mu.Lock()
	q.received++
	if len(q.batches) > 0 {
		next := q.batches[0]
		q.batches = q.batches[1:]
		q.mu.Unlock()
		return next, nil
	}
	q.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (q *fakeQueue) Delete(_ context.Context, m sqs.Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.deleted = append(q.deleted, m.ID)
	return nil
}

func (q *fakeQueue) ChangeVisibility(_ context.Context, m sqs.Message, d time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.visibility[m.ID] = d
	return nil
}

func (q *fakeQueue) DeadLetter(_ context.Context, m sqs.Message, reason, detail string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.deadErr != nil {
		return q.deadErr
	}
	q.dead[m.ID] = deadLetter{group: m.GroupID, reason: reason, detail: detail}
	return nil
}

func (q *fakeQueue) receives() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.received
}

type result struct {
	res app.MessageResult
	err error
}

type call struct {
	consumer  string
	messageID string
	cmd       app.SubmitWager
	ctxErr    error
}

type fakeService struct {
	mu      sync.Mutex
	results map[string]result
	calls   []call
	started chan struct{}
	proceed chan struct{}
}

func (s *fakeService) SubmitMessage(ctx context.Context, name, messageID string, cmd app.SubmitWager) (app.MessageResult, error) {
	if s.started != nil {
		close(s.started)
		s.started = nil
		<-s.proceed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call{consumer: name, messageID: messageID, cmd: cmd, ctxErr: ctx.Err()})
	if r, ok := s.results[messageID]; ok {
		return r.res, r.err
	}
	return app.MessageResult{Outcome: "PROCESSED", TransactionID: uuid.New()}, nil
}

func (s *fakeService) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, len(s.calls))
	for i, c := range s.calls {
		ids[i] = c.messageID
	}
	return ids
}

func newConsumer(t *testing.T, q *fakeQueue, s *fakeService, ping func(context.Context) error) (*consumer.Consumer, *prometheus.Registry) {
	t.Helper()
	q.visibility, q.dead = map[string]time.Duration{}, map[string]deadLetter{}
	if ping == nil {
		ping = func(context.Context) error { return nil }
	}
	reg := prometheus.NewRegistry()
	c, err := consumer.New(config.ConsumerConfig{Workers: 1, WaitTime: time.Second, MessageTimeout: time.Second},
		30*time.Second, q, s, ping, reg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return c, reg
}

func body(messageID string, edit func(env, data map[string]any)) string {
	data := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": "transaction-" + messageID,
		"idempotencyKey":        "provider-a:" + messageID,
		"playerId":              playerID,
		"walletId":              walletID,
		"roundId":               "round-987",
		"gameId":                "fortune-chimp",
		"kind":                  "BET",
		"money":                 map[string]any{"amount": "25.00", "currency": "BRL"},
	}
	env := map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": "2026-09-08T12:00:00.000Z",
		"data":       data,
	}
	if edit != nil {
		edit(env, data)
	}
	b, err := json.Marshal(env)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func message(id, group, body string, receiveCount int) sqs.Message {
	return sqs.Message{ID: "sqs-" + id, Body: body, ReceiptHandle: "rh-" + id, GroupID: group, ReceiveCount: receiveCount}
}

func metric(t *testing.T, reg *prometheus.Registry, name, label string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if label != "" && (len(m.GetLabel()) == 0 || m.GetLabel()[0].GetValue() != label) {
				continue
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
			return m.GetGauge().GetValue()
		}
	}
	return 0
}

func requireOutcomes(t *testing.T, reg *prometheus.Registry, want map[string]float64) {
	t.Helper()
	for _, outcome := range []string{"processed", "rejected", "pending_reference", "replayed", "duplicate", "retried", "dead_lettered", "released"} {
		if got := metric(t, reg, "wallet_consumer_messages_total", outcome); got != want[outcome] {
			t.Errorf("wallet_consumer_messages_total{outcome=%q} = %v; want %v", outcome, got, want[outcome])
		}
	}
}

func TestHandledMessagesAreDeleted(t *testing.T) {
	svc := &fakeService{results: map[string]result{
		"m-rejected":  {res: app.MessageResult{Outcome: "REJECTED", TransactionID: uuid.New()}},
		"m-pending":   {res: app.MessageResult{Outcome: "PENDING_REFERENCE", TransactionID: uuid.New()}},
		"m-replayed":  {res: app.MessageResult{Outcome: app.OutcomeReplayed, TransactionID: uuid.New()}},
		"m-duplicate": {res: app.MessageResult{Outcome: "PROCESSED", TransactionID: uuid.New(), Duplicate: true}},
	}}
	q := &fakeQueue{}
	c, reg := newConsumer(t, q, svc, nil)
	var batch []sqs.Message
	for _, id := range []string{"m-processed", "m-rejected", "m-pending", "m-replayed", "m-duplicate"} {
		batch = append(batch, message(id, walletID, body(id, nil), 1))
	}
	c.Handle(t.Context(), batch)

	if want := []string{"sqs-m-processed", "sqs-m-rejected", "sqs-m-pending", "sqs-m-replayed", "sqs-m-duplicate"}; !reflect.DeepEqual(q.deleted, want) {
		t.Fatalf("deleted %v; want %v", q.deleted, want)
	}
	if len(q.visibility) != 0 || len(q.dead) != 0 {
		t.Fatalf("visibility changes %v, dead letters %v; want none", q.visibility, q.dead)
	}
	for _, got := range svc.calls {
		cmd := got.cmd
		if got.consumer != consumer.Name || cmd.CorrelationID != got.messageID || cmd.Channel != app.ChannelSQS ||
			cmd.IdempotencyKey != "provider-a:"+got.messageID || cmd.ExternalTransactionID != "transaction-"+got.messageID || cmd.PayloadHash == "" {
			t.Fatalf("call %+v; want the SQS command for %s under consumer %s", got, got.messageID, consumer.Name)
		}
	}
	requireOutcomes(t, reg, map[string]float64{"processed": 1, "rejected": 1, "pending_reference": 1, "replayed": 1, "duplicate": 1})
}

func TestInvalidMessagesAreDeadLettered(t *testing.T) {
	valid := body("m-1", nil)
	tests := []struct {
		name   string
		body   string
		reason string
		detail string
	}{
		{"not JSON", `{"messageId":`, consumer.ReasonMalformed, "envelope"},
		{"trailing data", valid + ` {}`, consumer.ReasonMalformed, "trailing data"},
		{"unknown envelope field", body("m-1", func(env, _ map[string]any) { env["priority"] = 1 }), consumer.ReasonMalformed, "priority"},
		{"missing messageId", body("m-1", func(env, _ map[string]any) { delete(env, "messageId") }), consumer.ReasonMalformed, "messageId"},
		{"messageId with a space", body("m 1", nil), consumer.ReasonMalformed, "messageId"},
		{"missing occurredAt", body("m-1", func(env, _ map[string]any) { delete(env, "occurredAt") }), consumer.ReasonMalformed, "occurredAt"},
		{"occurredAt not RFC 3339", body("m-1", func(env, _ map[string]any) { env["occurredAt"] = "2026-09-08 12:00" }), consumer.ReasonMalformed, "envelope"},
		{"missing data", body("m-1", func(env, _ map[string]any) { delete(env, "data") }), consumer.ReasonMalformed, "data"},
		{"null data", body("m-1", func(env, _ map[string]any) { env["data"] = nil }), consumer.ReasonMalformed, "data"},
		{"another type", body("m-1", func(env, _ map[string]any) { env["type"] = "WagerTransactionCancelled" }), consumer.ReasonUnsupportedType, "WagerTransactionCancelled"},
		{"unknown data field", body("m-1", func(_, data map[string]any) { data["bonus"] = "yes" }), consumer.ReasonInvalidRequest, "bonus"},
		{"amount as a JSON number", body("m-1", func(_, data map[string]any) { data["money"] = map[string]any{"amount": 25.00, "currency": "BRL"} }), consumer.ReasonInvalidRequest, "money.amount"},
		{"OPENING", body("m-1", func(_, data map[string]any) { data["kind"] = "OPENING" }), consumer.ReasonInvalidRequest, "KIND_NOT_ALLOWED"},
		{"missing idempotency key", body("m-1", func(_, data map[string]any) { delete(data, "idempotencyKey") }), consumer.ReasonInvalidRequest, "idempotencyKey: REQUIRED"},
		{"several bad fields", body("m-1", func(_, data map[string]any) { data["walletId"], data["kind"] = "wallet-1", "" }), consumer.ReasonInvalidRequest, "walletId: INVALID_VALUE; kind: REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{}
			q := &fakeQueue{}
			c, reg := newConsumer(t, q, svc, nil)
			c.Handle(t.Context(), []sqs.Message{message("bad", "group-1", tt.body, 1)})
			got := q.dead["sqs-bad"]
			if got.reason != tt.reason || got.group != "group-1" || !strings.Contains(got.detail, tt.detail) || strings.Contains(got.detail, "\n") {
				t.Fatalf("dead letter %+v; want reason %s, group group-1 and a one-line detail mentioning %q", got, tt.reason, tt.detail)
			}
			if len(svc.calls) != 0 || !reflect.DeepEqual(q.deleted, []string{"sqs-bad"}) {
				t.Fatalf("use case calls %d, deleted %v; want none and the message deleted", len(svc.calls), q.deleted)
			}
			requireOutcomes(t, reg, map[string]float64{"dead_lettered": 1})
			if n := metric(t, reg, "wallet_consumer_dead_letters_total", tt.reason); n != 1 {
				t.Fatalf("dead letters for %s = %v; want 1", tt.reason, n)
			}
		})
	}
}

func TestUseCaseErrorsChooseTheDeadLetterReason(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{"unknown provider", fmt.Errorf("%w: provider-z", app.ErrUnknownProvider), "UNKNOWN_PROVIDER"},
		{"inbox hash mismatch", fmt.Errorf("%w: message m-1 was first received with another payload", app.ErrInboxHashMismatch), "INBOX_HASH_MISMATCH"},
		{"key reused", fmt.Errorf("%w: key k belongs to another payload", app.ErrIdempotencyKeyReused), "IDEMPOTENCY_KEY_REUSED"},
		{"external id under another key", fmt.Errorf("%w: under another key", app.ErrExternalIDConflict), "EXTERNAL_TRANSACTION_ID_CONFLICT"},
		{"corrupt stored state", fmt.Errorf("%w: corrupt stored state: %w", app.ErrPermanent, domain.ErrInvalidValue), consumer.ReasonProcessingFailed},
		{"unexpected error", errors.New("boom"), consumer.ReasonProcessingFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{results: map[string]result{"m-1": {err: tt.err}}}
			q := &fakeQueue{}
			c, reg := newConsumer(t, q, svc, nil)
			c.Handle(t.Context(), []sqs.Message{message("m-1", walletID, body("m-1", nil), 1)})
			if got := q.dead["sqs-m-1"]; got.reason != tt.reason || got.detail != tt.err.Error() {
				t.Fatalf("dead letter %+v; want reason %s with the error as detail", got, tt.reason)
			}
			if !reflect.DeepEqual(q.deleted, []string{"sqs-m-1"}) || len(q.visibility) != 0 {
				t.Fatalf("deleted %v, visibility %v; want the message deleted and nothing delayed", q.deleted, q.visibility)
			}
			requireOutcomes(t, reg, map[string]float64{"dead_lettered": 1})
		})
	}
}

func TestTransientFailuresDelayTheMessage(t *testing.T) {
	down := fmt.Errorf("%w: connection refused", app.ErrTransient)
	svc := &fakeService{results: map[string]result{
		"m-1":  {err: down},
		"m-2":  {err: app.ErrConcurrentUpdate},
		"m-5":  {err: fmt.Errorf("submit: %w", context.DeadlineExceeded)},
		"m-9":  {err: down},
		"m-12": {err: down},
	}}
	q := &fakeQueue{}
	c, reg := newConsumer(t, q, svc, nil)
	var batch []sqs.Message
	for _, n := range []int{1, 2, 5, 9, 12} {
		id := fmt.Sprintf("m-%d", n)
		batch = append(batch, message(id, "group-"+id, body(id, nil), n))
	}
	c.Handle(t.Context(), batch)

	want := map[string]time.Duration{"sqs-m-1": 2 * time.Second, "sqs-m-2": 4 * time.Second, "sqs-m-5": 32 * time.Second,
		"sqs-m-9": 5 * time.Minute, "sqs-m-12": 5 * time.Minute}
	if !reflect.DeepEqual(q.visibility, want) {
		t.Fatalf("visibility %v; want %v", q.visibility, want)
	}
	if len(q.deleted) != 0 || len(q.dead) != 0 {
		t.Fatalf("deleted %v, dead letters %v; a transient failure must leave the message in the queue", q.deleted, q.dead)
	}
	requireOutcomes(t, reg, map[string]float64{"retried": 5})
}

func TestAFailureHoldsBackTheRestOfItsGroup(t *testing.T) {
	svc := &fakeService{results: map[string]result{"a": {err: fmt.Errorf("%w: lock timeout", app.ErrTransient)}}}
	q := &fakeQueue{}
	c, reg := newConsumer(t, q, svc, nil)
	c.Handle(t.Context(), []sqs.Message{
		message("a", "g1", body("a", nil), 1),
		message("b", "g2", body("b", nil), 1),
		message("c", "g1", body("c", nil), 1),
		message("d", "g2", body("d", nil), 1),
	})
	if got := svc.called(); !reflect.DeepEqual(got, []string{"a", "b", "d"}) {
		t.Fatalf("use case called for %v; want a, b and d, never c after a failed in its group", got)
	}
	if want := map[string]time.Duration{"sqs-a": 2 * time.Second, "sqs-c": 0}; !reflect.DeepEqual(q.visibility, want) {
		t.Fatalf("visibility %v; want a delayed and c released at once", q.visibility)
	}
	if !reflect.DeepEqual(q.deleted, []string{"sqs-b", "sqs-d"}) {
		t.Fatalf("deleted %v; want b and d", q.deleted)
	}
	requireOutcomes(t, reg, map[string]float64{"processed": 2, "retried": 1, "released": 1})
}

func TestAFailedDeadLetterKeepsTheMessage(t *testing.T) {
	svc := &fakeService{}
	q := &fakeQueue{deadErr: errors.New("dlq unreachable")}
	c, reg := newConsumer(t, q, svc, nil)
	c.Handle(t.Context(), []sqs.Message{
		message("bad", "g1", "not json", 3),
		message("next", "g1", body("next", nil), 1),
	})
	if len(q.deleted) != 0 || len(svc.calls) != 0 {
		t.Fatalf("deleted %v, use case calls %d; want nothing deleted and nothing processed", q.deleted, len(svc.calls))
	}
	if want := map[string]time.Duration{"sqs-bad": 8 * time.Second, "sqs-next": 0}; !reflect.DeepEqual(q.visibility, want) {
		t.Fatalf("visibility %v; want the bad message delayed and the next one in its group released", q.visibility)
	}
	requireOutcomes(t, reg, map[string]float64{"retried": 1, "released": 1})
}

func TestStopFinishesTheCurrentMessageAndReleasesTheRest(t *testing.T) {
	svc := &fakeService{started: make(chan struct{}), proceed: make(chan struct{})}
	started := svc.started
	q := &fakeQueue{}
	c, reg := newConsumer(t, q, svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Handle(ctx, []sqs.Message{
			message("m-1", "g1", body("m-1", nil), 1),
			message("m-2", "g1", body("m-2", nil), 1),
			message("m-3", "g2", body("m-3", nil), 1),
		})
	}()
	<-started
	cancel()
	close(svc.proceed)
	<-done

	if len(svc.calls) != 1 || svc.calls[0].messageID != "m-1" || svc.calls[0].ctxErr != nil {
		t.Fatalf("calls %+v; want only m-1, finished on a context the stop did not cancel", svc.calls)
	}
	if !reflect.DeepEqual(q.deleted, []string{"sqs-m-1"}) {
		t.Fatalf("deleted %v; want the in-flight message", q.deleted)
	}
	if want := map[string]time.Duration{"sqs-m-2": 0, "sqs-m-3": 0}; !reflect.DeepEqual(q.visibility, want) {
		t.Fatalf("visibility %v; want the unprocessed messages released at once", q.visibility)
	}
	requireOutcomes(t, reg, map[string]float64{"processed": 1, "released": 2})
}

func TestNothingIsReceivedWhileTheDatabaseIsDown(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	ping := func(context.Context) error {
		if down.Load() {
			return errors.New("connection refused")
		}
		return nil
	}
	q := &fakeQueue{batches: [][]sqs.Message{{message("m-1", "g1", body("m-1", nil), 1)}}}
	c, reg := newConsumer(t, q, &fakeService{}, ping)
	for range 3 {
		if c.Tick(t.Context()) {
			t.Fatal("Tick received while the database was down")
		}
	}
	if q.receives() != 0 || metric(t, reg, "wallet_consumer_paused", "") != 1 {
		t.Fatalf("receives %d, paused %v; want no receive and the paused gauge at 1", q.receives(), metric(t, reg, "wallet_consumer_paused", ""))
	}
	down.Store(false)
	if !c.Tick(t.Context()) {
		t.Fatal("Tick did not receive once the database answered")
	}
	if q.receives() != 1 || !reflect.DeepEqual(q.deleted, []string{"sqs-m-1"}) || metric(t, reg, "wallet_consumer_paused", "") != 0 {
		t.Fatalf("receives %d, deleted %v, paused %v; want one receive, the message handled and the gauge back at 0",
			q.receives(), q.deleted, metric(t, reg, "wallet_consumer_paused", ""))
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	q := &fakeQueue{}
	c, _ := newConsumer(t, q, &fakeService{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for q.receives() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if q.receives() == 0 {
		t.Fatal("Run never polled")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRetryDelay(t *testing.T) {
	for count, want := range map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 4: 16 * time.Second, 8: 256 * time.Second, 9: 5 * time.Minute, 1000: 5 * time.Minute} {
		if got := consumer.RetryDelay(count); got != want {
			t.Errorf("RetryDelay(%d) = %s; want %s", count, got, want)
		}
	}
}

func TestMessagesContinueTheProducersTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
	q := &fakeQueue{}
	c, _ := newConsumer(t, q, &fakeService{}, nil)
	traced := message("m-1", "g1", body("m-1", nil), 1)
	traced.Attributes = map[string]string{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
	c.Handle(t.Context(), []sqs.Message{traced, message("m-2", "g2", "not json", 1)})

	ended := recorder.Ended()
	if len(ended) != 2 {
		t.Fatalf("%d spans; want one per message", len(ended))
	}
	attributes := func(s sdktrace.ReadOnlySpan) map[string]string {
		out := map[string]string{}
		for _, kv := range s.Attributes() {
			out[string(kv.Key)] = kv.Value.String()
		}
		return out
	}
	processed, malformed := ended[0], ended[1]
	if a := attributes(processed); processed.Name() != "process wager-transactions" || processed.SpanKind() != trace.SpanKindConsumer ||
		processed.SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || processed.Parent().SpanID().String() != "00f067aa0ba902b7" ||
		a["wallet.outcome"] != "processed" || a["messaging.message.id"] != "sqs-m-1" || a["messaging.system"] != "aws_sqs" {
		t.Fatalf("span %q in trace %s with %v; want a consumer span continuing the producer's trace", processed.Name(), processed.SpanContext().TraceID(), a)
	}
	if a := attributes(malformed); malformed.Parent().IsValid() || a["wallet.outcome"] != "dead_lettered" ||
		a["wallet.dead_letter_reason"] != consumer.ReasonMalformed || malformed.Status().Code != codes.Unset {
		t.Fatalf("span with parent %v, %v, %v; want a root span, dead-lettered as malformed, not an error", malformed.Parent(), a, malformed.Status())
	}
}
