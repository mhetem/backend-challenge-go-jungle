package tracing

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
)

const ServiceName = "wallet"

var Module = fx.Module("tracing",
	fx.Invoke(func(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) {
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
		if cfg.Tracing.Endpoint == "" {
			return
		}
		p := NewProvider(cfg.Tracing.Endpoint, cfg.InstanceID, log)
		lc.Append(fx.Hook{OnStart: p.Start, OnStop: p.Stop})
	}),
)

type Provider struct {
	endpoint  string
	instance  string
	transport *http.Transport
	sdk       *sdktrace.TracerProvider
	log       *slog.Logger
}

func NewProvider(endpoint, instance string, log *slog.Logger) *Provider {
	return &Provider{
		endpoint:  endpoint,
		instance:  instance,
		transport: http.DefaultTransport.(*http.Transport).Clone(),
		log:       log,
	}
}

func (p *Provider) Start(ctx context.Context) error {
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(p.endpoint+"/v1/traces"),
		otlptracehttp.WithHTTPClient(&http.Client{Transport: p.transport, Timeout: 10 * time.Second}),
	)
	if err != nil {
		return err
	}
	p.sdk = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName(ServiceName),
			semconv.ServiceInstanceID(p.instance),
		)),
	)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		p.log.Warn("exporting traces failed", "error", err)
	}))
	otel.SetTracerProvider(p.sdk)
	p.log.InfoContext(ctx, "tracing enabled", "endpoint", p.endpoint)
	return nil
}

func (p *Provider) Stop(ctx context.Context) error {
	if p.sdk == nil {
		return nil
	}
	err := p.sdk.Shutdown(ctx)
	p.transport.CloseIdleConnections()
	return err
}

func Start(ctx context.Context, scope, name string, kind trace.SpanKind, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(scope).Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
}

func WithTraceID(ctx context.Context) context.Context {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return logging.With(ctx, slog.String("traceId", sc.TraceID().String()))
	}
	return ctx
}

func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func Recording(ctx context.Context) bool {
	return trace.SpanFromContext(ctx).IsRecording()
}
