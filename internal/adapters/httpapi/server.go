package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
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

func NewServer(cfg config.Config, checker *health.Checker, authn auth.Authenticator, reg prometheus.Registerer, log *slog.Logger) (*Server, error) {
	protect, err := authentication(authn, reg, log)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	checker.Register(mux)
	mux.Handle("/", protect(http.HandlerFunc(notFound)))
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

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, http.StatusNotFound, "NOT_FOUND", "no such resource", false)
}
