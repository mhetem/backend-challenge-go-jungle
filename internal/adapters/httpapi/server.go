package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
)

var Module = fx.Module("httpapi",
	fx.Provide(
		func(s *app.WalletService) Wallets { return s },
		func(s *app.WagerService) Wagers { return s },
		NewServer,
	),
	fx.Invoke(func(lc fx.Lifecycle, cfg config.Config, s *Server) {
		if cfg.Enabled(config.HTTP) {
			lc.Append(fx.Hook{OnStart: s.Start, OnStop: s.Stop})
		}
	}),
)

type Server struct {
	*lifecycle.HTTPServer
}

func NewServer(
	cfg config.Config,
	checker *health.Checker,
	authn auth.Authenticator,
	wallets Wallets,
	wagers Wagers,
	reg prometheus.Registerer,
	log *slog.Logger,
) (*Server, error) {
	handler, err := newHandler(checker, authn, wallets, wagers, reg, log)
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

func newHandler(checker *health.Checker, authn auth.Authenticator, wallets Wallets, wagers Wagers, reg prometheus.Registerer, log *slog.Logger) (http.Handler, error) {
	protect, err := authentication(authn, reg, log)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	checker.Register(mux)
	(&api{wallets: wallets, wagers: wagers, log: log}).routes(mux, protect)
	return instrument(reg, recoverer(log, mux))
}
