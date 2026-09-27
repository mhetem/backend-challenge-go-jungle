package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/sqs"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/failpoint"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
)

const (
	Name          = "wager-transactions"
	MessageType   = "WagerTransactionRequested"
	settleTimeout = 5 * time.Second
	pauseInterval = time.Second
	maxRetryDelay = 5 * time.Minute
	maxDetail     = 1000
)

const (
	ReasonMalformed        = "MALFORMED_MESSAGE"
	ReasonUnsupportedType  = "UNSUPPORTED_MESSAGE_TYPE"
	ReasonInvalidRequest   = "INVALID_REQUEST"
	ReasonProcessingFailed = "PROCESSING_FAILED"
)

var conflicts = []*domain.Error{app.ErrUnknownProvider, app.ErrInboxHashMismatch, app.ErrIdempotencyKeyReused, app.ErrExternalIDConflict}

var Module = fx.Module("consumer",
	fx.Provide(
		func(c *sqs.Client) Queue { return c },
		func(s *app.WagerService) Service { return s },
		func(cfg config.Config, q Queue, s Service, pool *pgxpool.Pool, reg prometheus.Registerer, log *slog.Logger) (*Consumer, error) {
			return New(cfg.Consumer, cfg.SQS.VisibilityTimeout, q, s, pool.Ping, reg, log)
		},
	),
	fx.Invoke(func(lc fx.Lifecycle, cfg config.Config, c *Consumer, log *slog.Logger) {
		if cfg.Enabled(config.Consumer) {
			w := lifecycle.NewWorker("consumer", cfg.Consumer.Workers, c.Run, log)
			lc.Append(fx.Hook{OnStart: w.Start, OnStop: w.Stop})
		}
	}),
)

type Queue interface {
	Receive(ctx context.Context, wait, visibility time.Duration) ([]sqs.Message, error)
	Delete(ctx context.Context, m sqs.Message) error
	ChangeVisibility(ctx context.Context, m sqs.Message, d time.Duration) error
	DeadLetter(ctx context.Context, m sqs.Message, reason, detail string) error
}

type Service interface {
	SubmitMessage(ctx context.Context, consumer, messageID string, cmd app.SubmitWager) (app.MessageResult, error)
}

type Consumer struct {
	cfg         config.ConsumerConfig
	visibility  time.Duration
	queue       Queue
	service     Service
	ping        func(context.Context) error
	paused      atomic.Bool
	messages    *prometheus.CounterVec
	deadLetters *prometheus.CounterVec
	latency     prometheus.Histogram
	pausedGauge prometheus.Gauge
	log         *slog.Logger
}

func New(cfg config.ConsumerConfig, visibility time.Duration, queue Queue, service Service, ping func(context.Context) error,
	reg prometheus.Registerer, log *slog.Logger) (*Consumer, error) {
	c := &Consumer{
		cfg:        cfg,
		visibility: visibility,
		queue:      queue,
		service:    service,
		ping:       ping,
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "consumer_messages_total",
			Help:      "SQS messages by outcome: processed, rejected, pending_reference, replayed, duplicate, retried, dead_lettered or released.",
		}, []string{"outcome"}),
		deadLetters: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "consumer_dead_letters_total",
			Help:      "SQS messages the consumer moved to the dead-letter queue, by failure reason.",
		}, []string{"reason"}),
		latency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Name:      "consumer_processing_seconds",
			Help:      "Time from picking up an SQS message to settling it.",
			Buckets:   []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}),
		pausedGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Name:      "consumer_paused",
			Help:      "1 while the consumer stops receiving because the database does not answer.",
		}),
		log: log,
	}
	for _, m := range []prometheus.Collector{c.messages, c.deadLetters, c.latency, c.pausedGauge} {
		if err := reg.Register(m); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Consumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if !c.Tick(ctx) {
			sleep(ctx, pauseInterval)
		}
	}
}

func (c *Consumer) Tick(ctx context.Context) bool {
	if !c.ready(ctx) {
		return false
	}
	batch, err := c.queue.Receive(ctx, c.cfg.WaitTime, c.visibility)
	if err != nil {
		if ctx.Err() == nil {
			c.log.WarnContext(ctx, "receiving messages failed", "error", err)
		}
		return false
	}
	c.Handle(ctx, batch)
	return true
}

func (c *Consumer) ready(ctx context.Context) bool {
	probe, cancel := context.WithTimeout(ctx, health.ProbeTimeout)
	err := c.ping(probe)
	cancel()
	switch {
	case err == nil:
		if c.paused.Swap(false) {
			c.pausedGauge.Set(0)
			c.log.InfoContext(ctx, "database answers again; receiving messages")
		}
		return true
	case ctx.Err() == nil && !c.paused.Swap(true):
		c.pausedGauge.Set(1)
		c.log.WarnContext(ctx, "database does not answer; not receiving messages until it does", "error", err)
	}
	return false
}

func (c *Consumer) Handle(ctx context.Context, batch []sqs.Message) {
	blocked := map[string]bool{}
	var unprocessed []sqs.Message
	for _, m := range batch {
		if ctx.Err() != nil || blocked[m.GroupID] {
			unprocessed = append(unprocessed, m)
			continue
		}
		if !c.process(ctx, m) {
			blocked[m.GroupID] = true
		}
	}
	c.release(ctx, unprocessed)
}

func (c *Consumer) process(ctx context.Context, m sqs.Message) bool {
	start := time.Now()
	defer func() { c.latency.Observe(time.Since(start).Seconds()) }()
	ctx = logging.With(ctx, slog.String("sqsMessageId", m.ID))
	req, err := decode([]byte(m.Body))
	if err != nil {
		return c.deadLetter(ctx, m, err)
	}
	ctx = logging.With(ctx,
		slog.String("messageId", req.messageID),
		slog.String("correlationId", req.cmd.CorrelationID),
		slog.String("providerId", req.cmd.ProviderID),
		slog.String("walletId", req.cmd.WalletID.String()),
	)
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.MessageTimeout)
	res, err := c.service.SubmitMessage(work, Name, req.messageID, req.cmd)
	cancel()
	switch {
	case err == nil:
		failpoint.Hit(failpoint.ConsumerAfterCommit)
		return c.complete(ctx, m, res)
	case transient(err):
		return c.retry(ctx, m, err)
	}
	return c.deadLetter(ctx, m, err)
}

func (c *Consumer) complete(ctx context.Context, m sqs.Message, res app.MessageResult) bool {
	outcome := strings.ToLower(res.Outcome)
	if res.Duplicate {
		outcome = "duplicate"
	}
	ctx = logging.With(ctx, slog.String("transactionId", res.TransactionID.String()))
	c.messages.WithLabelValues(outcome).Inc()
	c.log.InfoContext(ctx, "message handled", "outcome", outcome)
	settle, cancel := settleContext(ctx)
	defer cancel()
	if err := c.queue.Delete(settle, m); err != nil {
		c.log.WarnContext(ctx, "deleting a handled message failed; it comes back and the inbox recognises it", "error", err)
	}
	return true
}

func (c *Consumer) retry(ctx context.Context, m sqs.Message, cause error) bool {
	delay := RetryDelay(m.ReceiveCount)
	c.messages.WithLabelValues("retried").Inc()
	c.log.WarnContext(ctx, "handling failed transiently; the message comes back after a delay",
		"error", cause, "receiveCount", m.ReceiveCount, "delay", delay)
	settle, cancel := settleContext(ctx)
	defer cancel()
	if err := c.queue.ChangeVisibility(settle, m, delay); err != nil {
		c.log.WarnContext(ctx, "delaying the message failed; it comes back when its visibility expires", "error", err)
	}
	return false
}

func (c *Consumer) deadLetter(ctx context.Context, m sqs.Message, cause error) bool {
	reason := reasonFor(cause)
	settle, cancel := settleContext(ctx)
	defer cancel()
	if err := c.queue.DeadLetter(settle, m, reason, detail(cause)); err != nil {
		return c.retry(ctx, m, fmt.Errorf("sending to the dead-letter queue (%s: %v): %w", reason, cause, err))
	}
	c.messages.WithLabelValues("dead_lettered").Inc()
	c.deadLetters.WithLabelValues(reason).Inc()
	level := slog.LevelWarn
	if reason == ReasonProcessingFailed {
		level = slog.LevelError
	}
	c.log.Log(ctx, level, "message moved to the dead-letter queue", "reason", reason, "error", cause)
	if err := c.queue.Delete(settle, m); err != nil {
		c.log.WarnContext(ctx, "deleting a dead-lettered message failed; it comes back and is dead-lettered again", "error", err)
	}
	return true
}

func (c *Consumer) release(ctx context.Context, msgs []sqs.Message) {
	if len(msgs) == 0 {
		return
	}
	settle, cancel := settleContext(ctx)
	defer cancel()
	for _, m := range msgs {
		if err := c.queue.ChangeVisibility(settle, m, 0); err != nil {
			c.log.WarnContext(ctx, "releasing an unprocessed message failed; it comes back when its visibility expires",
				"sqsMessageId", m.ID, "error", err)
			continue
		}
		c.messages.WithLabelValues("released").Inc()
	}
}

func RetryDelay(receiveCount int) time.Duration {
	return min(time.Second<<min(max(receiveCount, 0), 9), maxRetryDelay)
}

func transient(err error) bool {
	return errors.Is(err, app.ErrTransient) || errors.Is(err, app.ErrRetryableConflict) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func reasonFor(err error) string {
	var r *rejection
	switch {
	case errors.As(err, &r):
		return r.reason
	case errors.Is(err, app.ErrPermanent):
		return ReasonProcessingFailed
	}
	for _, known := range conflicts {
		if errors.Is(err, known) {
			return known.Code
		}
	}
	return ReasonProcessingFailed
}

func detail(err error) string {
	s := strings.ReplaceAll(err.Error(), "\n", "; ")
	if len(s) > maxDetail {
		s = s[:maxDetail]
	}
	return strings.ToValidUTF8(s, "")
}

func settleContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

type rejection struct {
	reason string
	err    error
}

func (r *rejection) Error() string {
	return r.err.Error()
}

func (r *rejection) Unwrap() error {
	return r.err
}

type envelope struct {
	MessageID  string          `json:"messageId"`
	Type       string          `json:"type"`
	OccurredAt time.Time       `json:"occurredAt"`
	Data       json.RawMessage `json:"data"`
}

type request struct {
	messageID string
	cmd       app.SubmitWager
}

func decode(body []byte) (request, error) {
	var env envelope
	if err := strictly(body, &env); err != nil {
		return request{}, &rejection{ReasonMalformed, fmt.Errorf("envelope: %w", err)}
	}
	switch {
	case env.MessageID == "" || !app.ValidToken(env.MessageID):
		return request{}, &rejection{ReasonMalformed, errors.New("messageId must be 1-128 visible ASCII characters")}
	case env.OccurredAt.IsZero():
		return request{}, &rejection{ReasonMalformed, errors.New("occurredAt is required")}
	case len(env.Data) == 0 || string(env.Data) == "null":
		return request{}, &rejection{ReasonMalformed, errors.New("data is required")}
	case env.Type != MessageType:
		return request{}, &rejection{ReasonUnsupportedType, fmt.Errorf("type %q is not %s", env.Type, MessageType)}
	}
	var data app.WagerTransactionRequestedData
	if err := strictly(env.Data, &data); err != nil {
		return request{}, &rejection{ReasonInvalidRequest, fmt.Errorf("data: %w", err)}
	}
	cmd, err := data.Command(env.MessageID)
	if err != nil {
		return request{}, &rejection{ReasonInvalidRequest, err}
	}
	return request{messageID: env.MessageID, cmd: cmd}, nil
}

func strictly(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing data after the JSON object")
	}
	return nil
}
