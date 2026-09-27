package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
)

const (
	challenge        = `Bearer realm="wagering"`
	invalidChallenge = `Bearer realm="wagering", error="invalid_token", error_description="the access token is invalid or expired"`
)

func authentication(authn auth.Authenticator, reg prometheus.Registerer, log *slog.Logger) (func(http.Handler) http.Handler, error) {
	failures := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metrics.Namespace,
		Name:      "auth_failures_total",
		Help:      "Requests turned away by authentication, by reason.",
	}, []string{"reason"})
	if err := reg.Register(failures); err != nil {
		return nil, err
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearer(r.Header.Get("Authorization"))
			if !ok {
				failures.WithLabelValues("missing").Inc()
				w.Header().Set("WWW-Authenticate", challenge)
				newProblem(http.StatusUnauthorized, "UNAUTHENTICATED", categoryAuthentication, "a bearer access token is required").write(w)
				return
			}
			principal, err := authn.Authenticate(r.Context(), token)
			switch {
			case errors.Is(err, auth.ErrUnavailable):
				failures.WithLabelValues("unavailable").Inc()
				log.WarnContext(r.Context(), "cannot verify tokens: signing keys unavailable", "error", err)
				newProblem(http.StatusServiceUnavailable, "TEMPORARILY_UNAVAILABLE", categoryTransient, "token verification is temporarily unavailable").write(w)
				return
			case err != nil:
				failures.WithLabelValues("invalid").Inc()
				log.InfoContext(r.Context(), "rejected an access token", "error", err)
				w.Header().Set("WWW-Authenticate", invalidChallenge)
				newProblem(http.StatusUnauthorized, "UNAUTHENTICATED", categoryAuthentication, "the access token is invalid or expired").write(w)
				return
			}
			ctx := auth.WithPrincipal(r.Context(), principal)
			ctx = logging.With(ctx, slog.String("clientId", principal.ClientID), slog.String("providerId", principal.ProviderID))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}, nil
}

func bearer(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}
