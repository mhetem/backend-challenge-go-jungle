package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
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

func TestRequestsAreTracedUnderTheCallersTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
	var logs bytes.Buffer
	log := logging.New(&logs, slog.LevelInfo)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /wallets/{walletId}", func(w http.ResponseWriter, r *http.Request) {
		log.InfoContext(r.Context(), "handled")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	handler, err := instrument(prometheus.NewRegistry(), mux)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/wallets/w-1", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	ended := recorder.Ended()
	if len(ended) != 2 {
		t.Fatalf("%d spans; want one per request", len(ended))
	}
	wallet, boom := ended[0], ended[1]
	attrs := map[string]string{}
	for _, kv := range wallet.Attributes() {
		attrs[string(kv.Key)] = kv.Value.String()
	}
	switch {
	case wallet.Name() != "GET /wallets/{walletId}" || wallet.SpanKind() != trace.SpanKindServer:
		t.Fatalf("span %q of kind %v; want the route as a server span", wallet.Name(), wallet.SpanKind())
	case wallet.SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || wallet.Parent().SpanID().String() != "00f067aa0ba902b7" || !wallet.Parent().IsRemote():
		t.Fatalf("span in trace %s under %s; want it continuing the caller's traceparent", wallet.SpanContext().TraceID(), wallet.Parent().SpanID())
	case attrs["http.route"] != "GET /wallets/{walletId}" || attrs["http.response.status_code"] != "200" || wallet.Status().Code != codes.Unset:
		t.Fatalf("attributes %v, status %v", attrs, wallet.Status())
	case boom.Name() != "GET /boom" || boom.Status().Code != codes.Error:
		t.Fatalf("span %q with %v; want a 500 marked as an error", boom.Name(), boom.Status())
	case !strings.Contains(logs.String(), `"traceId":"4bf92f3577b34da6a3ce929d0e0e4736"`):
		t.Fatalf("handler log %q; want it tagged with the trace id", logs.String())
	}
}
