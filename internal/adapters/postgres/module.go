package postgres

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
)

var Module = fx.Module("postgres",
	fx.Provide(
		configFrom,
		func(cfg Config) (*pgxpool.Pool, error) { return NewPool(context.Background(), cfg) },
		fx.Annotate(NewTxRunner, fx.As(new(app.TxRunner))),
		fx.Annotate(check, fx.ResultTags(`group:"health.checks"`)),
	),
	fx.Invoke(func(lc fx.Lifecycle, pool *pgxpool.Pool, log *slog.Logger) {
		lc.Append(fx.Hook{
			OnStart: func(ctx context.Context) error {
				return lifecycle.Retry(ctx, log, "postgres", pool.Ping)
			},
			OnStop: func(context.Context) error {
				pool.Close()
				return nil
			},
		})
	}),
)

func configFrom(cfg config.Config) Config {
	return Config{
		URL:              cfg.Database.URL,
		ApplicationName:  "wallet/" + cfg.InstanceID,
		MaxConns:         cfg.Database.MaxConns,
		LockTimeout:      cfg.Database.LockTimeout,
		StatementTimeout: cfg.Database.StatementTimeout,
		TxAttempts:       cfg.Database.TxAttempts,
		TxBackoff:        cfg.Database.TxBackoff,
	}
}

func check(pool *pgxpool.Pool) health.Check {
	return health.Check{Name: "postgres", Probe: pool.Ping}
}
