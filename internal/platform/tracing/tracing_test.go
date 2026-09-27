package tracing_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/goleak"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/tracing"
)

func TestProviderExportsSpansAndStopsCleanly(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	var (
		mu    sync.Mutex
		paths []string
	)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	p := tracing.NewProvider(sink.URL, "test-instance", slog.New(slog.DiscardHandler))
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, span := tracing.Start(t.Context(), "test", "unit of work", trace.SpanKindInternal)
	span.End()
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 || paths[0] != "POST /v1/traces application/x-protobuf" {
		t.Fatalf("exporter requests %v; want the span posted to /v1/traces on shutdown", paths)
	}
}

func TestStopWithoutStartIsANoOp(t *testing.T) {
	if err := tracing.NewProvider("http://127.0.0.1:1", "x", slog.New(slog.DiscardHandler)).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWithTraceIDAddsTheTraceToLogs(t *testing.T) {
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	remote := trace.ContextWithRemoteSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true}))
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo)
	log.InfoContext(tracing.WithTraceID(remote), "traced")
	log.InfoContext(tracing.WithTraceID(context.Background()), "untraced")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"traceId":"4bf92f3577b34da6a3ce929d0e0e4736"`) || strings.Contains(lines[1], "traceId") {
		t.Fatalf("log lines %q; want the trace id only on the traced one", lines)
	}
}

func TestEndMarksErrors(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)).Tracer("test")
	_, ok := tracer.Start(context.Background(), "ok")
	tracing.End(ok, nil)
	_, bad := tracer.Start(context.Background(), "bad")
	tracing.End(bad, errors.New("boom"))
	ended := recorder.Ended()
	if len(ended) != 2 || ended[0].Status().Code != codes.Unset || ended[1].Status().Code != codes.Error || len(ended[1].Events()) != 1 {
		t.Fatalf("statuses %v, %v; want unset and an error with the recorded exception", ended[0].Status(), ended[1].Status())
	}
	if tracing.Recording(trace.ContextWithSpan(context.Background(), ok)) || tracing.Recording(context.Background()) {
		t.Fatal("an ended span, or no span at all, must not count as recording")
	}
}
