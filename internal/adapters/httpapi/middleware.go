package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/tracing"
)

const traceScope = "wallet/httpapi"

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func instrument(reg prometheus.Registerer, next http.Handler) (http.Handler, error) {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metrics.Namespace,
		Name:      "http_requests_total",
		Help:      "HTTP requests by method, route pattern and status code.",
	}, []string{"method", "route", "status"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metrics.Namespace,
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency by method and route pattern.",
		Buckets:   []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"method", "route"})
	for _, c := range []prometheus.Collector{requests, duration} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracing.Start(ctx, traceScope, r.Method, trace.SpanKindServer,
			semconv.HTTPRequestMethodKey.String(r.Method), semconv.URLPath(r.URL.Path))
		r = r.WithContext(tracing.WithTraceID(ctx))
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			} else {
				span.SetName(route)
				span.SetAttributes(semconv.HTTPRoute(route))
			}
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			span.SetAttributes(semconv.HTTPResponseStatusCode(rec.status))
			if rec.status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(rec.status))
			}
			span.End()
			requests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
			duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(rec, r)
	}), nil
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			log.ErrorContext(r.Context(), "handler panicked", "method", r.Method, "route", r.Pattern, "panic", v)
			if rec.status == 0 {
				http.Error(rec, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(rec, r)
	})
}
