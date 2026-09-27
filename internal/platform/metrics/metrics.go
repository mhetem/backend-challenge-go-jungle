package metrics

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
)

const Namespace = "wallet"

var Module = fx.Module("metrics",
	fx.Provide(
		NewRegistry,
		func(r *prometheus.Registry) prometheus.Registerer { return r },
		NewApp,
		NewAdmin,
	),
	fx.Invoke(func(lc fx.Lifecycle, admin *Admin, _ *App) {
		lc.Append(fx.Hook{OnStart: admin.Start, OnStop: admin.Stop})
	}),
)

func NewRegistry() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}

type App struct {
	reconciliationDivergences prometheus.Counter
}

func NewApp(reg prometheus.Registerer) (*App, error) {
	m := &App{
		reconciliationDivergences: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "reconciliation_divergences_total",
			Help:      "Reconciliations that found a stored balance or version chain diverging from the ledger.",
		}),
	}
	if err := reg.Register(m.reconciliationDivergences); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *App) ReconciliationDiverged() {
	m.reconciliationDivergences.Inc()
}

type Admin struct {
	*lifecycle.HTTPServer
}

func NewAdmin(cfg config.Config, reg *prometheus.Registry, checker *health.Checker, log *slog.Logger) *Admin {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	checker.Register(mux)
	return &Admin{lifecycle.NewHTTPServer("admin", &http.Server{
		Addr:              cfg.AdminAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}, log)}
}
