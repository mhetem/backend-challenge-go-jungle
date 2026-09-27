package postgres

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/tracing"
)

var _ app.TxRunner = (*TxRunner)(nil)

type TxRunner struct {
	pool      *pgxpool.Pool
	attempts  int
	backoff   time.Duration
	readWrite pgx.TxOptions
	snapshot  pgx.TxOptions
	retries   *prometheus.CounterVec
}

func NewTxRunner(pool *pgxpool.Pool, cfg Config) *TxRunner {
	timeouts := fmt.Sprintf("SET LOCAL lock_timeout = %d; SET LOCAL statement_timeout = %d",
		cfg.LockTimeout.Milliseconds(), cfg.StatementTimeout.Milliseconds())
	return &TxRunner{
		pool:      pool,
		attempts:  cfg.TxAttempts,
		backoff:   cfg.TxBackoff,
		readWrite: pgx.TxOptions{BeginQuery: "BEGIN ISOLATION LEVEL READ COMMITTED; " + timeouts},
		snapshot:  pgx.TxOptions{BeginQuery: "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; " + timeouts},
	}
}

func (r *TxRunner) Instrument(reg prometheus.Registerer) error {
	r.retries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metrics.Namespace,
		Name:      "db_transaction_retries_total",
		Help:      "Transactions rerun after a concurrency conflict, by reason: serialization, deadlock, lock_timeout or conflict.",
	}, []string{"reason"})
	return reg.Register(r.retries)
}

func (r *TxRunner) InTx(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return r.run(ctx, "db transaction", r.readWrite, fn)
}

func (r *TxRunner) InReadOnlySnapshot(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return r.run(ctx, "db snapshot", r.snapshot, fn)
}

func (r *TxRunner) run(ctx context.Context, name string, opts pgx.TxOptions, fn func(context.Context, app.Store) error) error {
	ctx, span := transactionSpan(ctx, name)
	for attempt := 1; ; attempt++ {
		err := r.once(ctx, opts, fn)
		reason := retryReason(err)
		if err == nil || attempt >= r.attempts || reason == "" {
			span.SetAttributes(attribute.Int("db.transaction.attempts", attempt))
			tracing.End(span, err)
			return err
		}
		if sleep(ctx, r.delay(attempt)) != nil {
			tracing.End(span, err)
			return err
		}
		span.AddEvent("retry", trace.WithAttributes(attribute.String("reason", reason), attribute.Int("attempt", attempt)))
		if r.retries != nil {
			r.retries.WithLabelValues(reason).Inc()
		}
	}
}

func (r *TxRunner) once(ctx context.Context, opts pgx.TxOptions, fn func(context.Context, app.Store) error) error {
	tx, err := r.pool.BeginTx(ctx, opts)
	if err != nil {
		return classify(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(ctx, store{q: database.New(tx)}); err != nil {
		return err
	}
	return classify(tx.Commit(ctx))
}

func (r *TxRunner) delay(attempt int) time.Duration {
	d := r.backoff << (attempt - 1)
	return d/2 + rand.N(d/2+1)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
