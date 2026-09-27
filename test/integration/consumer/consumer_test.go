//go:build integration

package consumer_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres"
	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/sqs"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/workers/consumer"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/sqstest"
)

var discard = slog.New(slog.DiscardHandler)

type noMetrics struct{}

func (noMetrics) ReconciliationDiverged() {}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type harness struct {
	t      *testing.T
	db     *dbtest.Database
	queues *sqstest.Queues
	client *sqs.Client
	pool   *pgxpool.Pool
	wagers *app.WagerService
	w      wallet.Snapshot
}

func setup(t *testing.T, maxReceiveCount int, lockTimeout time.Duration) *harness {
	t.Helper()
	h := &harness{t: t, db: dbtest.New(t), queues: sqstest.New(t, maxReceiveCount)}
	cfg, err := config.Parse(func(key string) (string, bool) {
		switch key {
		case "SQS_INPUT_QUEUE":
			return h.queues.Input, true
		case "SQS_INPUT_DLQ":
			return h.queues.DLQ, true
		}
		return os.LookupEnv(key)
	})
	must(t, err)
	h.client, err = sqs.New(cfg, discard)
	must(t, err)
	must(t, h.client.Start(t.Context()))
	t.Cleanup(func() { _ = h.client.Stop(context.Background()) })

	pc := postgres.Config{URL: h.db.AppURL, ApplicationName: "consumer-test", MaxConns: 8,
		LockTimeout: lockTimeout, StatementTimeout: 10 * time.Second, TxAttempts: 1, TxBackoff: 5 * time.Millisecond}
	h.pool, err = postgres.NewPool(context.Background(), pc)
	must(t, err)
	t.Cleanup(h.pool.Close)
	runner := postgres.NewTxRunner(h.pool, pc)
	rules, err := wager.NewRules(time.Minute, 20, func(int) time.Duration { return time.Second })
	must(t, err)
	h.wagers = app.NewWagerService(runner, rules, app.SystemClock, app.NewID)

	cmd, err := app.OpenWalletRequest{
		PlayerID:       uuid.NewString(),
		InitialBalance: app.MoneyRequest{Amount: "100.00", Currency: "BRL"},
	}.Command("corr-open")
	must(t, err)
	h.w, err = app.NewWalletService(runner, app.SystemClock, app.NewID, discard, noMetrics{}).Open(t.Context(), cmd)
	must(t, err)
	return h
}

func (h *harness) consumer(service consumer.Service) (*consumer.Consumer, *prometheus.Registry) {
	h.t.Helper()
	reg := prometheus.NewRegistry()
	c, err := consumer.New(config.ConsumerConfig{Workers: 1, WaitTime: time.Second, MessageTimeout: 5 * time.Second},
		30*time.Second, h.client, service, h.pool.Ping, reg, discard)
	must(h.t, err)
	return c, reg
}

func (h *harness) run(c *consumer.Consumer) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	stop := func() {
		cancel()
		<-done
	}
	h.t.Cleanup(stop)
	return stop
}

type wagerMessage struct {
	id       string
	provider string
	key      string
	kind     string
	extID    string
	amount   string
	ref      string
}

func (h *harness) envelope(m wagerMessage) string {
	h.t.Helper()
	if m.provider == "" {
		m.provider = "provider-a"
	}
	if m.key == "" {
		m.key = m.provider + ":" + m.extID
	}
	data := map[string]any{
		"providerId":            m.provider,
		"externalTransactionId": m.extID,
		"idempotencyKey":        m.key,
		"playerId":              h.w.PlayerID.String(),
		"walletId":              h.w.ID.String(),
		"roundId":               "round-1",
		"gameId":                "game-1",
		"kind":                  m.kind,
		"money":                 map[string]string{"amount": m.amount, "currency": "BRL"},
	}
	if m.ref != "" {
		data["referenceExternalTransactionId"] = m.ref
	}
	b, err := json.Marshal(map[string]any{
		"messageId":  m.id,
		"type":       consumer.MessageType,
		"occurredAt": "2026-09-27T12:00:00.000Z",
		"data":       data,
	})
	must(h.t, err)
	return string(b)
}

func (h *harness) send(body, dedup string) {
	h.t.Helper()
	h.queues.Send(body, h.w.ID.String(), dedup)
}

type inboxRow struct {
	messageID     string
	outcome       string
	transactionID uuid.UUID
}

func (h *harness) inbox() []inboxRow {
	h.t.Helper()
	rows, err := h.db.App.Query(h.t.Context(), `SELECT message_id, outcome, transaction_id FROM inbox_messages
		WHERE consumer_name = $1 ORDER BY message_id`, consumer.Name)
	must(h.t, err)
	defer rows.Close()
	var out []inboxRow
	for rows.Next() {
		var r inboxRow
		must(h.t, rows.Scan(&r.messageID, &r.outcome, &r.transactionID))
		out = append(out, r)
	}
	must(h.t, rows.Err())
	return out
}

type txRow struct {
	id     uuid.UUID
	extID  string
	status string
	code   string
}

func (h *harness) transactions() []txRow {
	h.t.Helper()
	rows, err := h.db.App.Query(h.t.Context(), `SELECT id, external_transaction_id, status, coalesce(failure_code, '')
		FROM wager_transactions WHERE kind <> 'OPENING' ORDER BY external_transaction_id`)
	must(h.t, err)
	defer rows.Close()
	var out []txRow
	for rows.Next() {
		var r txRow
		must(h.t, rows.Scan(&r.id, &r.extID, &r.status, &r.code))
		out = append(out, r)
	}
	must(h.t, rows.Err())
	return out
}

func (h *harness) balance() string {
	h.t.Helper()
	var balance string
	must(h.t, h.db.App.QueryRow(h.t.Context(), `SELECT (balance_minor / 100.0)::numeric(20,2)::text FROM wallets WHERE id = $1`, h.w.ID).Scan(&balance))
	return balance
}

func (h *harness) debits() int {
	h.t.Helper()
	var n int
	must(h.t, h.db.App.QueryRow(h.t.Context(), `SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, h.w.ID).Scan(&n))
	return n
}

func (h *harness) requireInputEmpty() {
	h.t.Helper()
	if visible, inFlight := h.queues.Counts(h.queues.InputURL); visible != 0 || inFlight != 0 {
		h.t.Fatalf("input queue holds %d visible and %d in-flight messages; want it empty", visible, inFlight)
	}
}

func count(reg *prometheus.Registry, name, label string) int {
	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if m.GetLabel()[0].GetValue() == label {
				return int(m.GetCounter().GetValue())
			}
		}
	}
	return 0
}

func outcomes(reg *prometheus.Registry) map[string]int {
	out := map[string]int{}
	for _, o := range []string{"processed", "rejected", "pending_reference", "replayed", "duplicate", "retried", "dead_lettered", "released"} {
		if n := count(reg, "wallet_consumer_messages_total", o); n > 0 {
			out[o] = n
		}
	}
	return out
}

func eventually(t *testing.T, wait time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func settled(reg *prometheus.Registry) int {
	n := 0
	for _, v := range outcomes(reg) {
		n += v
	}
	return n
}

func TestDuplicateDeliveriesAreDedupedByTheInbox(t *testing.T) {
	t.Parallel()
	h := setup(t, 10, 2*time.Second)
	bet := h.envelope(wagerMessage{id: "msg-1", kind: "BET", extID: "bet-1", amount: "25.00"})
	h.send(bet, "dedup-1")
	h.send(bet, "dedup-2")

	c, reg := h.consumer(h.wagers)
	stop := h.run(c)
	eventually(t, 15*time.Second, "both deliveries", func() bool { return settled(reg) == 2 })
	stop()

	if got := outcomes(reg); got["processed"] != 1 || got["duplicate"] != 1 || len(got) != 2 {
		t.Fatalf("outcomes %v; want one processed and one duplicate: the copy reached the consumer and the inbox stopped it", got)
	}
	txs := h.transactions()
	if len(txs) != 1 || txs[0].status != "PROCESSED" {
		t.Fatalf("transactions %+v; want the bet processed once", txs)
	}
	if got := h.inbox(); len(got) != 1 || got[0] != (inboxRow{"msg-1", "PROCESSED", txs[0].id}) {
		t.Fatalf("inbox %+v; want msg-1 completed as PROCESSED with the bet's id", got)
	}
	if h.debits() != 1 || h.balance() != "75.00" {
		t.Fatalf("%d debits, balance %s; want one debit and 75.00", h.debits(), h.balance())
	}
	h.requireInputEmpty()
}

func TestSameKeyUnderANewMessageReplays(t *testing.T) {
	t.Parallel()
	h := setup(t, 10, 2*time.Second)
	viaHTTP, err := app.WagerRequest{
		ProviderID:            "provider-a",
		ExternalTransactionID: "bet-2",
		PlayerID:              h.w.PlayerID.String(),
		WalletID:              h.w.ID.String(),
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  "BET",
		Money:                 app.MoneyRequest{Amount: "10.00", Currency: "BRL"},
	}.Command("provider-a:bet-2", "corr-http")
	must(t, err)
	first, err := h.wagers.Submit(t.Context(), viaHTTP)
	must(t, err)

	bet := wagerMessage{kind: "BET", extID: "bet-1", amount: "25.00"}
	bet.id = "msg-1"
	h.send(h.envelope(bet), "dedup-1")
	bet.id = "msg-2"
	h.send(h.envelope(bet), "dedup-2")
	h.send(h.envelope(wagerMessage{id: "msg-3", kind: "BET", extID: "bet-2", amount: "10.00"}), "dedup-3")

	c, reg := h.consumer(h.wagers)
	stop := h.run(c)
	eventually(t, 15*time.Second, "three messages", func() bool { return settled(reg) == 3 })
	stop()

	if got := outcomes(reg); got["processed"] != 1 || got["replayed"] != 2 || len(got) != 2 {
		t.Fatalf("outcomes %v; want one processed and two replays", got)
	}
	txs := h.transactions()
	if len(txs) != 2 || txs[0].extID != "bet-1" || txs[1].id != first.Transaction.ID {
		t.Fatalf("transactions %+v; want bet-1 from SQS and bet-2 from the HTTP submission, nothing else", txs)
	}
	want := []inboxRow{
		{"msg-1", "PROCESSED", txs[0].id},
		{"msg-2", app.OutcomeReplayed, txs[0].id},
		{"msg-3", app.OutcomeReplayed, first.Transaction.ID},
	}
	if got := h.inbox(); !slices.Equal(got, want) {
		t.Fatalf("inbox %+v; want %+v", got, want)
	}
	if h.debits() != 2 || h.balance() != "65.00" {
		t.Fatalf("%d debits, balance %s; want two debits and 65.00", h.debits(), h.balance())
	}
	h.requireInputEmpty()
}

func TestBusinessOutcomesAreDeleted(t *testing.T) {
	t.Parallel()
	h := setup(t, 10, 2*time.Second)
	h.send(h.envelope(wagerMessage{id: "msg-1", kind: "BET", extID: "bet-1", amount: "150.00"}), "dedup-1")
	h.send(h.envelope(wagerMessage{id: "msg-2", kind: "ROLLBACK", extID: "rollback-1", amount: "25.00", ref: "bet-2"}), "dedup-2")

	c, reg := h.consumer(h.wagers)
	stop := h.run(c)
	eventually(t, 15*time.Second, "two messages", func() bool { return settled(reg) == 2 })
	stop()

	if got := outcomes(reg); got["rejected"] != 1 || got["pending_reference"] != 1 || len(got) != 2 {
		t.Fatalf("outcomes %v; want one rejection and one pending reference", got)
	}
	txs := h.transactions()
	if len(txs) != 2 || txs[0] != (txRow{txs[0].id, "bet-1", "REJECTED", "INSUFFICIENT_FUNDS"}) ||
		txs[1] != (txRow{txs[1].id, "rollback-1", "PENDING_REFERENCE", ""}) {
		t.Fatalf("transactions %+v; want bet-1 REJECTED INSUFFICIENT_FUNDS and rollback-1 PENDING_REFERENCE", txs)
	}
	want := []inboxRow{{"msg-1", "REJECTED", txs[0].id}, {"msg-2", "PENDING_REFERENCE", txs[1].id}}
	if got := h.inbox(); !slices.Equal(got, want) {
		t.Fatalf("inbox %+v; want %+v", got, want)
	}
	if h.debits() != 0 || h.balance() != "100.00" {
		t.Fatalf("%d debits, balance %s; want none and 100.00", h.debits(), h.balance())
	}
	h.requireInputEmpty()
}

func TestInvalidMessagesGoToTheDLQ(t *testing.T) {
	t.Parallel()
	h := setup(t, 10, 2*time.Second)
	bet := wagerMessage{id: "msg-1", kind: "BET", extID: "bet-1", amount: "25.00"}
	changed := bet
	changed.amount = "30.00"
	reused := changed
	reused.id, reused.extID, reused.key = "msg-6", "bet-6", "provider-a:bet-1"
	unsupported := h.envelope(wagerMessage{id: "msg-4", kind: "BET", extID: "bet-4", amount: "1.00"})
	var env map[string]any
	must(t, json.Unmarshal([]byte(unsupported), &env))
	env["type"] = "WagerTransactionCancelled"
	b, err := json.Marshal(env)
	must(t, err)
	unsupported = string(b)

	sent := []struct {
		body   string
		reason string
	}{
		{h.envelope(bet), ""},
		{`{"messageId":"msg-2","type":`, consumer.ReasonMalformed},
		{h.envelope(wagerMessage{id: "msg-3", kind: "OPENING", extID: "open-3", amount: "5.00"}), consumer.ReasonInvalidRequest},
		{unsupported, consumer.ReasonUnsupportedType},
		{h.envelope(wagerMessage{id: "msg-5", provider: "provider-z", kind: "BET", extID: "bet-5", amount: "1.00"}), app.ErrUnknownProvider.Code},
		{h.envelope(changed), app.ErrInboxHashMismatch.Code},
		{h.envelope(reused), app.ErrIdempotencyKeyReused.Code},
	}
	for i, m := range sent {
		h.send(m.body, "dedup-"+string(rune('a'+i)))
	}

	c, reg := h.consumer(h.wagers)
	stop := h.run(c)
	eventually(t, 15*time.Second, "every message", func() bool { return settled(reg) == len(sent) })
	stop()

	if got := outcomes(reg); got["processed"] != 1 || got["dead_lettered"] != len(sent)-1 || len(got) != 2 {
		t.Fatalf("outcomes %v; want one processed and %d dead-lettered", got, len(sent)-1)
	}
	dead := h.queues.Drain(h.queues.DLQURL, len(sent)-1, 10*time.Second)
	if len(dead) != len(sent)-1 {
		t.Fatalf("the DLQ holds %d messages; want %d", len(dead), len(sent)-1)
	}
	for i, m := range sent[1:] {
		got := dead[i]
		if got.Body != m.body || got.GroupID != h.w.ID.String() || got.Attributes["failureReason"] != m.reason || got.Attributes["failureDetail"] == "" {
			t.Fatalf("dead letter %d = %+v; want the original body in group %s with reason %s and a detail", i, got, h.w.ID, m.reason)
		}
	}
	txs := h.transactions()
	if len(txs) != 1 || txs[0].extID != "bet-1" {
		t.Fatalf("transactions %+v; want only bet-1", txs)
	}
	if got := h.inbox(); len(got) != 1 || got[0].messageID != "msg-1" {
		t.Fatalf("inbox %+v; want only msg-1", got)
	}
	if h.balance() != "75.00" {
		t.Fatalf("balance %s; want 75.00", h.balance())
	}
	h.requireInputEmpty()
}

func TestTransientFailuresEndInTheDLQ(t *testing.T) {
	t.Parallel()
	h := setup(t, 2, 300*time.Millisecond)
	holder, err := h.db.App.Begin(t.Context())
	must(t, err)
	defer func() { _ = holder.Rollback(context.Background()) }()
	_, err = holder.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, h.w.ID)
	must(t, err)
	body := h.envelope(wagerMessage{id: "msg-1", kind: "BET", extID: "bet-1", amount: "25.00"})
	h.send(body, "dedup-1")

	c, reg := h.consumer(h.wagers)
	stop := h.run(c)
	dead := h.queues.Drain(h.queues.DLQURL, 1, 20*time.Second)
	stop()
	must(t, holder.Rollback(t.Context()))

	if len(dead) != 1 || dead[0].Body != body || dead[0].Attributes["failureReason"] != "" {
		t.Fatalf("DLQ %+v; want the message moved by the redrive policy after two receives, without a consumer failure reason", dead)
	}
	if got := outcomes(reg); got["retried"] != 2 || len(got) != 1 {
		t.Fatalf("outcomes %v; want exactly two retries and nothing else", got)
	}
	if txs, inbox := h.transactions(), h.inbox(); len(txs) != 0 || len(inbox) != 0 || h.balance() != "100.00" {
		t.Fatalf("transactions %+v, inbox %+v, balance %s; want nothing written", txs, inbox, h.balance())
	}
	h.requireInputEmpty()
}

type gated struct {
	consumer.Service
	once    sync.Once
	started chan struct{}
	proceed chan struct{}
}

func (g *gated) SubmitMessage(ctx context.Context, name, messageID string, cmd app.SubmitWager) (app.MessageResult, error) {
	g.once.Do(func() {
		close(g.started)
		<-g.proceed
	})
	return g.Service.SubmitMessage(ctx, name, messageID, cmd)
}

func TestStopFinishesTheInFlightMessageAndReleasesTheRest(t *testing.T) {
	t.Parallel()
	h := setup(t, 10, 2*time.Second)
	for i := 1; i <= 5; i++ {
		id := string(rune('0' + i))
		h.send(h.envelope(wagerMessage{id: "msg-" + id, kind: "BET", extID: "bet-" + id, amount: "10.00"}), "dedup-"+id)
	}

	svc := &gated{Service: h.wagers, started: make(chan struct{}), proceed: make(chan struct{})}
	c, reg := h.consumer(svc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-svc.started:
	case <-time.After(15 * time.Second):
		t.Fatal("the consumer never picked up a message")
	}
	cancel()
	close(svc.proceed)
	<-done

	if got := outcomes(reg); got["processed"] != 1 || got["released"] != 4 || len(got) != 2 {
		t.Fatalf("outcomes %v; want the in-flight message processed and the other four released", got)
	}
	if got := h.inbox(); len(got) != 1 || got[0].messageID != "msg-1" {
		t.Fatalf("inbox %+v; want only msg-1", got)
	}
	if visible, inFlight := h.queues.Counts(h.queues.InputURL); visible != 4 || inFlight != 0 {
		t.Fatalf("input queue: %d visible, %d in flight; want the four released messages visible at once", visible, inFlight)
	}

	next, regNext := h.consumer(h.wagers)
	stopNext := h.run(next)
	eventually(t, 15*time.Second, "the released messages", func() bool { return settled(regNext) == 4 })
	stopNext()
	if got := outcomes(regNext); got["processed"] != 4 || len(got) != 1 {
		t.Fatalf("outcomes after restart %v; want the four remaining processed", got)
	}
	if got := h.inbox(); len(got) != 5 || h.debits() != 5 || h.balance() != "50.00" {
		t.Fatalf("inbox %+v, %d debits, balance %s; want five messages, five debits and 50.00", got, h.debits(), h.balance())
	}
	h.requireInputEmpty()
}

func TestTheProducersTraceReachesTheEvents(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	h := setup(t, 10, 2*time.Second)
	const traceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	h.queues.SendWith(h.envelope(wagerMessage{id: "msg-1", kind: "BET", extID: "bet-1", amount: "25.00"}), h.w.ID.String(), "dedup-1",
		map[string]string{"traceparent": traceParent})

	c, reg := h.consumer(h.wagers)
	stop := h.run(c)
	eventually(t, 15*time.Second, "the message", func() bool { return settled(reg) == 1 })
	stop()

	var traced int
	must(t, h.db.App.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events o JOIN wager_transactions w ON o.aggregate_id IN (w.id, w.wallet_id)
		WHERE w.external_transaction_id = 'bet-1' AND o.occurred_at >= w.created_at
		AND split_part(o.trace_parent, '-', 2) = '4bf92f3577b34da6a3ce929d0e0e4736'`).Scan(&traced))
	if traced != 2 {
		t.Fatalf("%d of the bet's events carry the producer's trace; want both (processed and balance changed)", traced)
	}
}
