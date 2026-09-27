package resolver

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
)

var Module = fx.Module("resolver",
	fx.Provide(
		func(s *app.WagerService) Service { return s },
		func(cfg config.Config, s Service, reg prometheus.Registerer, log *slog.Logger) (*Resolver, error) {
			return New(cfg.Resolver, s, reg, log)
		},
	),
	fx.Invoke(func(lc fx.Lifecycle, cfg config.Config, r *Resolver, log *slog.Logger) {
		if cfg.Enabled(config.Resolver) {
			w := lifecycle.NewWorker("resolver", 1, r.Run, log)
			lc.Append(fx.Hook{OnStart: w.Start, OnStop: w.Stop})
		}
	}),
)

type Service interface {
	Due(ctx context.Context, limit int) ([]app.DueTransaction, error)
	Resolve(ctx context.Context, due app.DueTransaction) (app.Resolution, error)
	Fail(ctx context.Context, due app.DueTransaction) (bool, error)
}

type Resolver struct {
	service     Service
	interval    time.Duration
	batch       int
	maxFailures int
	failures    map[uuid.UUID]int
	outcomes    *prometheus.CounterVec
	log         *slog.Logger
}

func New(cfg config.ResolverConfig, service Service, reg prometheus.Registerer, log *slog.Logger) (*Resolver, error) {
	outcomes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metrics.Namespace,
		Name:      "resolver_outcomes_total",
		Help:      "Pending-reference attempts by outcome: processed, rejected, rescheduled, skipped, failed or error.",
	}, []string{"outcome"})
	if err := reg.Register(outcomes); err != nil {
		return nil, err
	}
	return &Resolver{
		service:     service,
		interval:    cfg.PollInterval,
		batch:       cfg.BatchSize,
		maxFailures: cfg.MaxFailures,
		failures:    map[uuid.UUID]int{},
		outcomes:    outcomes,
		log:         log,
	}, nil
}

func (r *Resolver) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Resolver) Tick(ctx context.Context) {
	due, err := r.service.Due(ctx, r.batch)
	if err != nil {
		if ctx.Err() == nil {
			r.log.WarnContext(ctx, "listing due pending references failed", "error", err)
		}
		return
	}
	stillDue := make(map[uuid.UUID]bool, len(due))
	for _, d := range due {
		stillDue[d.ID] = true
	}
	for id := range r.failures {
		if !stillDue[id] {
			delete(r.failures, id)
		}
	}
	for _, d := range due {
		if ctx.Err() != nil {
			return
		}
		r.resolve(ctx, d)
	}
}

func (r *Resolver) resolve(ctx context.Context, d app.DueTransaction) {
	ctx = logging.With(ctx, slog.String("transactionId", d.ID.String()), slog.String("walletId", d.WalletID.String()))
	res, err := r.service.Resolve(ctx, d)
	switch {
	case err == nil:
		delete(r.failures, d.ID)
		r.outcomes.WithLabelValues(outcome(res)).Inc()
		if !res.Skipped && res.Status != wager.PendingReference {
			r.log.InfoContext(ctx, "pending reference settled", "status", res.Status)
		}
	case ctx.Err() != nil || errors.Is(err, app.ErrTransient) || errors.Is(err, app.ErrRetryableConflict):
		r.outcomes.WithLabelValues("error").Inc()
		r.log.WarnContext(ctx, "resolving failed transiently; retrying next tick", "error", err)
	default:
		r.failures[d.ID]++
		r.outcomes.WithLabelValues("error").Inc()
		r.log.ErrorContext(ctx, "resolving failed", "error", err, "consecutiveFailures", r.failures[d.ID])
		if r.failures[d.ID] >= r.maxFailures {
			r.fail(ctx, d)
		}
	}
}

func (r *Resolver) fail(ctx context.Context, d app.DueTransaction) {
	failed, err := r.service.Fail(ctx, d)
	if err != nil {
		r.log.ErrorContext(ctx, "recording the permanent failure failed; retrying next tick", "error", err)
		return
	}
	delete(r.failures, d.ID)
	if failed {
		r.outcomes.WithLabelValues("failed").Inc()
		r.log.ErrorContext(ctx, "pending reference marked FAILED after repeated permanent errors", "failureCode", wager.ProcessingFailed)
	}
}

func outcome(res app.Resolution) string {
	switch {
	case res.Skipped:
		return "skipped"
	case res.Status == wager.PendingReference:
		return "rescheduled"
	case res.Status == wager.Processed:
		return "processed"
	case res.Status == wager.Rejected:
		return "rejected"
	}
	return "error"
}
