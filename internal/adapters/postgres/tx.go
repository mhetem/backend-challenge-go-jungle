package postgres

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
)

var _ app.TxRunner = (*TxRunner)(nil)

type TxRunner struct {
	pool      *pgxpool.Pool
	attempts  int
	backoff   time.Duration
	readWrite pgx.TxOptions
	snapshot  pgx.TxOptions
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

func (r *TxRunner) InTx(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return r.run(ctx, r.readWrite, fn)
}

func (r *TxRunner) InReadOnlySnapshot(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return r.run(ctx, r.snapshot, fn)
}

func (r *TxRunner) run(ctx context.Context, opts pgx.TxOptions, fn func(context.Context, app.Store) error) error {
	for attempt := 1; ; attempt++ {
		err := r.once(ctx, opts, fn)
		if err == nil || attempt >= r.attempts || !retryable(err) {
			return err
		}
		if sleep(ctx, r.delay(attempt)) != nil {
			return err
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
