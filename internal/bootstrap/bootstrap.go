package bootstrap

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/httpapi"
	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/oidc"
	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres"
	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/sqs"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
	"github.com/mhetem/backend-challenge-go-jungle/internal/workers/consumer"
	"github.com/mhetem/backend-challenge-go-jungle/internal/workers/outbox"
	"github.com/mhetem/backend-challenge-go-jungle/internal/workers/resolver"
)

func Options(cfg config.Config) fx.Option {
	return fx.Options(
		fx.Supply(cfg),
		fx.StartTimeout(cfg.StartTimeout),
		fx.StopTimeout(cfg.ShutdownTimeout),
		fx.WithLogger(logging.FxLogger),
		logging.Module,
		health.Module,
		postgres.Module,
		sqs.Module,
		oidc.Module,
		metrics.Module,
		appModule,
		httpapi.Module,
		resolver.Module,
		outbox.Module,
		consumer.Module,
		fx.Invoke(announce),
	)
}

var appModule = fx.Module("app",
	fx.Provide(
		func() app.Clock { return app.SystemClock },
		func() app.IDs { return app.NewID },
		rules,
		func(m *metrics.App) app.Metrics { return m },
		app.NewWalletService,
		app.NewWagerService,
	),
)

func rules(cfg config.Config) (wager.Rules, error) {
	p := cfg.PendingReference
	return wager.NewRules(p.TTL, p.MaxAttempts, app.ExponentialBackoff(p.BackoffBase, p.BackoffCap))
}

func announce(lc fx.Lifecycle, cfg config.Config, checker *health.Checker, log *slog.Logger) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			log.InfoContext(ctx, "wallet service started", "config", cfg)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			checker.Drain()
			log.InfoContext(ctx, "draining: readiness now reports unavailable")
			return nil
		},
	})
}
