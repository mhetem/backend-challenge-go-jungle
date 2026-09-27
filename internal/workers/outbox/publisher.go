package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/sqs"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/failpoint"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/tracing"
)

const (
	markTimeout = 5 * time.Second
	traceScope  = "wallet/outbox"
)

var Module = fx.Module("outbox",
	fx.Provide(
		func(c *sqs.Client) Sender { return c },
		func(cfg config.Config, tx app.TxRunner, s Sender, clock app.Clock, reg prometheus.Registerer, log *slog.Logger) (*Publisher, error) {
			return New(cfg.Outbox, cfg.InstanceID+"/"+app.NewID().String(), tx, s, clock, reg, log)
		},
	),
	fx.Invoke(func(lc fx.Lifecycle, cfg config.Config, p *Publisher, log *slog.Logger) {
		if cfg.Enabled(config.Outbox) {
			w := lifecycle.NewWorker("outbox", 1, p.Run, log)
			lc.Append(fx.Hook{OnStart: w.Start, OnStop: w.Stop})
		}
	}),
)

type Sender interface {
	PublishEvents(ctx context.Context, msgs []app.OutboxMessage) []error
}

type Publisher struct {
	cfg      config.OutboxConfig
	owner    string
	tx       app.TxRunner
	sender   Sender
	clock    app.Clock
	backoff  func(attempt int) time.Duration
	results  *prometheus.CounterVec
	attempts prometheus.Histogram
	pending  prometheus.Gauge
	oldest   prometheus.Gauge
	log      *slog.Logger
}

func New(cfg config.OutboxConfig, owner string, tx app.TxRunner, sender Sender, clock app.Clock, reg prometheus.Registerer, log *slog.Logger) (*Publisher, error) {
	p := &Publisher{
		cfg:     cfg,
		owner:   owner,
		tx:      tx,
		sender:  sender,
		clock:   clock,
		backoff: app.ExponentialBackoff(cfg.BackoffBase, cfg.BackoffCap),
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "outbox_publish_results_total",
			Help:      "Outbox events handled by result: published, failed (released with backoff) or lost (claim taken over).",
		}, []string{"result"}),
		attempts: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Name:      "outbox_publish_attempts",
			Help:      "Claims an event needed before it was published.",
			Buckets:   []float64{1, 2, 3, 5, 10, 20},
		}),
		pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Name:      "outbox_pending",
			Help:      "Outbox events not yet published.",
		}),
		oldest: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Name:      "outbox_oldest_pending_age_seconds",
			Help:      "Age of the oldest unpublished outbox event.",
		}),
		log: log.With("owner", owner),
	}
	for _, c := range []prometheus.Collector{p.results, p.attempts, p.pending, p.oldest} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	refresh := 10 * p.cfg.PollInterval
	var observed time.Time
	for {
		for p.Tick(ctx) == p.cfg.BatchSize && ctx.Err() == nil {
			if time.Since(observed) >= refresh {
				p.Observe(ctx)
				observed = time.Now()
			}
		}
		p.Observe(ctx)
		observed = time.Now()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Publisher) Tick(ctx context.Context) int {
	now := p.clock()
	var claimed []app.OutboxMessage
	err := p.tx.InTx(ctx, func(ctx context.Context, st app.Store) error {
		var err error
		claimed, err = st.Outbox().Claim(ctx, p.owner, now, now.Add(p.cfg.Lease), p.cfg.BatchSize)
		return err
	})
	if err != nil {
		if ctx.Err() == nil {
			p.log.WarnContext(ctx, "claiming outbox events failed", "error", err)
		}
		return 0
	}
	if len(claimed) > 0 {
		failpoint.Hit(failpoint.OutboxAfterClaim)
	}
	for chunk := range slices.Chunk(claimed, sqs.MaxBatch) {
		p.publish(ctx, chunk)
	}
	return len(claimed)
}

type settled struct {
	result   string
	attempts int
}

func (p *Publisher) publish(ctx context.Context, chunk []app.OutboxMessage) {
	links := make([]trace.Link, 0, len(chunk))
	for _, m := range chunk {
		origin := otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier{"traceparent": m.TraceParent})
		if sc := trace.SpanContextFromContext(origin); sc.IsValid() {
			links = append(links, trace.Link{SpanContext: sc})
		}
	}
	ctx, span := otel.Tracer(traceScope).Start(ctx, "publish events", trace.WithSpanKind(trace.SpanKindProducer), trace.WithLinks(links...),
		trace.WithAttributes(semconv.MessagingSystemAWSSQS, semconv.MessagingOperationTypeSend, attribute.Int("messaging.batch.message_count", len(chunk))))
	defer span.End()
	ctx = tracing.WithTraceID(ctx)
	errs := p.sender.PublishEvents(ctx, chunk)
	failpoint.Hit(failpoint.OutboxAfterPublish)
	now := p.clock()
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), markTimeout)
	defer cancel()
	var done []settled
	err := p.tx.InTx(markCtx, func(ctx context.Context, st app.Store) error {
		done = done[:0]
		for i, m := range chunk {
			var (
				ok  bool
				err error
			)
			result := "published"
			if errs[i] == nil {
				ok, err = st.Outbox().MarkPublished(ctx, m.ID, p.owner, now)
			} else {
				result = "failed"
				next := now.Add(min(p.backoff(m.Attempts), p.cfg.BackoffCap))
				ok, err = st.Outbox().Release(ctx, m.ID, p.owner, next, errs[i].Error())
			}
			if err != nil {
				return err
			}
			if !ok {
				result = "lost"
			}
			done = append(done, settled{result: result, attempts: m.Attempts})
		}
		return nil
	})
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		p.log.WarnContext(ctx, "recording publish results failed; the claims expire and the events are retried", "error", err, "events", len(chunk))
		return
	}
	failed := 0
	for _, e := range errs {
		if e != nil {
			failed++
		}
	}
	span.SetAttributes(attribute.Int("wallet.outbox.failed", failed))
	if failed > 0 {
		span.SetStatus(codes.Error, fmt.Sprintf("%d of %d events not published", failed, len(chunk)))
	}
	for i, s := range done {
		p.results.WithLabelValues(s.result).Inc()
		switch s.result {
		case "published":
			p.attempts.Observe(float64(s.attempts))
		case "failed":
			p.log.WarnContext(ctx, "publishing an outbox event failed; backing off",
				"eventId", chunk[i].ID, "eventType", chunk[i].EventType, "attempts", s.attempts, "error", errs[i])
		}
	}
}

func (p *Publisher) Observe(ctx context.Context) {
	var (
		pending int64
		oldest  time.Time
	)
	err := p.tx.InReadOnlySnapshot(ctx, func(ctx context.Context, st app.Store) error {
		var err error
		pending, oldest, err = st.Outbox().Backlog(ctx)
		return err
	})
	if err != nil {
		return
	}
	p.pending.Set(float64(pending))
	age := 0.0
	if !oldest.IsZero() {
		age = max(p.clock().Sub(oldest).Seconds(), 0)
	}
	p.oldest.Set(age)
}
