package app

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

const traceScope = "wallet/app"

func startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(traceScope).Start(ctx, name, trace.WithAttributes(attrs...))
}

func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		var de *domain.Error
		if !errors.As(err, &de) || errors.Is(err, ErrPermanent) {
			span.SetStatus(codes.Error, err.Error())
		}
	}
	span.End()
}

func commandAttributes(cmd SubmitWager) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("wager.provider_id", cmd.ProviderID),
		attribute.String("wager.external_transaction_id", cmd.ExternalTransactionID),
		attribute.String("wager.kind", string(cmd.Kind)),
		attribute.String("wager.wallet_id", cmd.WalletID.String()),
		attribute.String("wager.channel", string(cmd.Channel)),
	}
}

func transactionAttributes(s wager.Snapshot) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("wager.transaction_id", s.ID.String()),
		attribute.String("wager.status", string(s.Status)),
	}
	if s.FailureCode != "" {
		attrs = append(attrs, attribute.String("wager.failure_code", string(s.FailureCode)))
	}
	return attrs
}
