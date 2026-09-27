package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/tracing"
)

const traceScope = "wallet/postgres"

type querySpanKey struct{}

type queryTracer struct{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !tracing.Recording(ctx) {
		return ctx
	}
	name := operation(data.SQL)
	ctx, span := tracing.Start(ctx, traceScope, name, trace.SpanKindClient,
		semconv.DBSystemNamePostgreSQL, semconv.DBOperationName(name), semconv.DBQueryText(data.SQL))
	return context.WithValue(ctx, querySpanKey{}, span)
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(querySpanKey{}).(trace.Span)
	if !ok {
		return
	}
	span.SetAttributes(attribute.Int64("db.rows_affected", data.CommandTag.RowsAffected()))
	tracing.End(span, data.Err)
}

func operation(sql string) string {
	if rest, ok := strings.CutPrefix(sql, "-- name: "); ok {
		if name, _, ok := strings.Cut(rest, " "); ok {
			return name
		}
	}
	if fields := strings.Fields(sql); len(fields) > 0 {
		return strings.ToUpper(strings.TrimSuffix(fields[0], ";"))
	}
	return "query"
}

func transactionSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	if !tracing.Recording(ctx) {
		return ctx, trace.SpanFromContext(context.Background())
	}
	return tracing.Start(ctx, traceScope, name, trace.SpanKindInternal, semconv.DBSystemNamePostgreSQL)
}
