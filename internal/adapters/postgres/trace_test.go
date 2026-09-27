package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestOperationNamesTheSpan(t *testing.T) {
	for sql, want := range map[string]string{
		"-- name: ClaimOutboxEvents :many\nUPDATE outbox_events SET claimed_by = $1": "ClaimOutboxEvents",
		"BEGIN ISOLATION LEVEL READ COMMITTED; SET LOCAL lock_timeout = 2000":        "BEGIN",
		"commit":   "COMMIT",
		"rollback": "ROLLBACK",
		"   ":      "query",
	} {
		if got := operation(sql); got != want {
			t.Errorf("operation(%q) = %q; want %q", sql, got, want)
		}
	}
}

func TestQueryTracerOnlyTracesUnderARecordingSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
	tracer := queryTracer{}

	untraced := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "commit"})
	tracer.TraceQueryEnd(untraced, nil, pgx.TraceQueryEndData{})
	if n := len(recorder.Ended()); n != 0 {
		t.Fatalf("%d spans without a recording parent; want none", n)
	}

	parent, span := otel.Tracer("test").Start(context.Background(), "use case")
	ctx := tracer.TraceQueryStart(parent, nil, pgx.TraceQueryStartData{SQL: "-- name: InsertOutboxEvent :exec\nINSERT INTO outbox_events"})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("INSERT 0 1")})
	failed := tracer.TraceQueryStart(parent, nil, pgx.TraceQueryStartData{SQL: "-- name: UpdateWalletBalance :execrows\nUPDATE wallets"})
	tracer.TraceQueryEnd(failed, nil, pgx.TraceQueryEndData{Err: errors.New("deadlock detected")})
	span.End()

	ended := recorder.Ended()
	if len(ended) != 3 {
		t.Fatalf("%d spans; want two queries and their parent", len(ended))
	}
	insert, update := ended[0], ended[1]
	if insert.Name() != "InsertOutboxEvent" || insert.Parent().SpanID() != span.SpanContext().SpanID() || insert.Status().Code != codes.Unset {
		t.Fatalf("insert span %s under %s with %v", insert.Name(), insert.Parent().SpanID(), insert.Status())
	}
	if update.Name() != "UpdateWalletBalance" || update.Status().Code != codes.Error {
		t.Fatalf("update span %s with %v; want an error status", update.Name(), update.Status())
	}
	attrs := map[string]string{}
	for _, kv := range insert.Attributes() {
		attrs[string(kv.Key)] = kv.Value.String()
	}
	if attrs["db.system.name"] != "postgresql" || attrs["db.operation.name"] != "InsertOutboxEvent" || attrs["db.rows_affected"] != "1" {
		t.Fatalf("insert attributes %v", attrs)
	}
}
