package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
)

var Module = fx.Module("logging",
	fx.Provide(func(cfg config.Config) *slog.Logger {
		return New(os.Stderr, cfg.LogLevel).With("instanceId", cfg.InstanceID)
	}),
)

func FxLogger(log *slog.Logger) fxevent.Logger {
	l := &fxevent.SlogLogger{Logger: log}
	l.UseLogLevel(slog.LevelDebug)
	return l
}

var sensitive = []string{"authorization", "password", "secret", "token", "cookie"}

func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(contextHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redact,
	})})
}

type attrsKey struct{}

func With(ctx context.Context, attrs ...slog.Attr) context.Context {
	prev, _ := ctx.Value(attrsKey{}).([]slog.Attr)
	return context.WithValue(ctx, attrsKey{}, append(prev[:len(prev):len(prev)], attrs...))
}

type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(attrsKey{}).([]slog.Attr); ok {
		r.AddAttrs(attrs...)
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}

func redact(_ []string, a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	for _, word := range sensitive {
		if strings.Contains(key, word) {
			return slog.String(a.Key, "[REDACTED]")
		}
	}
	return a
}
