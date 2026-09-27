package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMiddlewareRecordsRoutesAndRecoversPanics(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /wallets/{walletId}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	reg := prometheus.NewRegistry()
	handler, err := instrument(reg, recoverer(slog.New(slog.DiscardHandler), mux))
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		path   string
		status int
	}{
		{"/wallets/a", http.StatusNotFound},
		{"/wallets/b", http.StatusNotFound},
		{"/ok", http.StatusOK},
		{"/boom", http.StatusInternalServerError},
		{"/nowhere", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != tt.status {
			t.Fatalf("GET %s = %d; want %d", tt.path, rec.Code, tt.status)
		}
	}

	want := `
# HELP wallet_http_requests_total HTTP requests by method, route pattern and status code.
# TYPE wallet_http_requests_total counter
wallet_http_requests_total{method="GET",route="GET /boom",status="500"} 1
wallet_http_requests_total{method="GET",route="GET /ok",status="200"} 1
wallet_http_requests_total{method="GET",route="GET /wallets/{walletId}",status="404"} 2
wallet_http_requests_total{method="GET",route="unmatched",status="404"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "wallet_http_requests_total"); err != nil {
		t.Fatal(err)
	}
	if n, err := testutil.GatherAndCount(reg, "wallet_http_request_duration_seconds"); err != nil || n != 4 {
		t.Fatalf("%d latency series (%v); want one per route", n, err)
	}
}
