package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
)

type fakeAuthenticator struct{}

func (fakeAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "good":
		return auth.Principal{ClientID: "provider-a", ProviderID: "provider-a", Roles: []auth.Role{auth.WagerProvider}}, nil
	case "down":
		return auth.Principal{}, fmt.Errorf("%w: connection refused", auth.ErrUnavailable)
	}
	return auth.Principal{}, fmt.Errorf("%w: bad signature", auth.ErrUnauthenticated)
}

func TestAuthentication(t *testing.T) {
	reg := prometheus.NewRegistry()
	protect, err := authentication(fakeAuthenticator{}, reg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	var seen []auth.Principal
	handler := protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		seen = append(seen, p)
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name          string
		authorization string
		status        int
		challenge     string
		code          string
	}{
		{"no header", "", http.StatusUnauthorized, challenge, "UNAUTHENTICATED"},
		{"basic scheme", "Basic cHJvdmlkZXItYTpzZWNyZXQ=", http.StatusUnauthorized, challenge, "UNAUTHENTICATED"},
		{"empty bearer", "Bearer ", http.StatusUnauthorized, challenge, "UNAUTHENTICATED"},
		{"invalid token", "Bearer forged", http.StatusUnauthorized, invalidChallenge, "UNAUTHENTICATED"},
		{"keys unavailable", "Bearer down", http.StatusServiceUnavailable, "", "TEMPORARILY_UNAVAILABLE"},
		{"valid token", "Bearer good", http.StatusNoContent, "", ""},
		{"scheme is case-insensitive", "bearer  good ", http.StatusNoContent, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/wallets/w-1", nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.status || rec.Header().Get("WWW-Authenticate") != tt.challenge {
				t.Fatalf("status %d, challenge %q; want %d, %q", rec.Code, rec.Header().Get("WWW-Authenticate"), tt.status, tt.challenge)
			}
			if tt.code == "" {
				return
			}
			var body problem
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			retryable := tt.status == http.StatusServiceUnavailable
			if body.Code != tt.code || body.Status != tt.status || body.Retryable != retryable ||
				rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("problem = %+v (%s)", body, rec.Header().Get("Content-Type"))
			}
			if retryable && rec.Header().Get("Retry-After") == "" {
				t.Fatal("503 without Retry-After")
			}
		})
	}

	if len(seen) != 2 || seen[0].ProviderID != "provider-a" || seen[1].ClientID != "provider-a" {
		t.Fatalf("handler saw principals %+v; want provider-a twice", seen)
	}
	want := `
# HELP wallet_auth_failures_total Requests turned away by authentication, by reason.
# TYPE wallet_auth_failures_total counter
wallet_auth_failures_total{reason="invalid"} 1
wallet_auth_failures_total{reason="missing"} 3
wallet_auth_failures_total{reason="unavailable"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "wallet_auth_failures_total"); err != nil {
		t.Fatal(err)
	}
}
