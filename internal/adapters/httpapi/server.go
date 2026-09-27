package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
)

var Module = fx.Module("httpapi",
	fx.Provide(NewServer),
	fx.Invoke(func(lc fx.Lifecycle, cfg config.Config, s *Server) {
		if cfg.Enabled(config.HTTP) {
			lc.Append(fx.Hook{OnStart: s.Start, OnStop: s.Stop})
		}
	}),
)

type Server struct {
	*lifecycle.HTTPServer
}

func NewServer(cfg config.Config, checker *health.Checker, reg prometheus.Registerer, log *slog.Logger) (*Server, error) {
	mux := http.NewServeMux()
	checker.Register(mux)
	handler, err := instrument(reg, recoverer(log, mux))
	if err != nil {
		return nil, err
	}
	return &Server{lifecycle.NewHTTPServer("http", &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}, log)}, nil
}
