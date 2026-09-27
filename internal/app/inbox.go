package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/failpoint"
)

const OutcomeReplayed = "REPLAYED"

type MessageResult struct {
	Outcome       string
	TransactionID uuid.UUID
	Duplicate     bool
}

func (s *WagerService) SubmitMessage(ctx context.Context, consumer, messageID string, cmd SubmitWager) (MessageResult, error) {
	ctx, span := startSpan(ctx, "WagerService.SubmitMessage", append(commandAttributes(cmd), attribute.String("wager.message_id", messageID))...)
	var result MessageResult
	err := s.tx.InTx(ctx, func(ctx context.Context, st Store) error {
		var err error
		result, err = s.submitMessage(ctx, st, consumer, messageID, cmd, s.clock())
		return err
	})
	if err != nil {
		endSpan(span, err)
		return MessageResult{}, err
	}
	span.SetAttributes(
		attribute.String("wager.transaction_id", result.TransactionID.String()),
		attribute.String("wager.inbox_outcome", result.Outcome),
		attribute.Bool("wager.duplicate", result.Duplicate),
	)
	endSpan(span, nil)
	if !result.Duplicate && result.Outcome == string(wager.PendingReference) {
		failpoint.Hit(failpoint.UsecaseAfterPendingReferenceCommit)
	}
	return result, nil
}

func (s *WagerService) submitMessage(ctx context.Context, st Store, consumer, messageID string, cmd SubmitWager, now time.Time) (MessageResult, error) {
	received := InboxMessage{Consumer: consumer, MessageID: messageID, PayloadHash: MessageHash(cmd), ReceivedAt: now}
	prior, err := st.Inbox().Receive(ctx, received)
	switch {
	case err != nil:
		return MessageResult{}, err
	case prior != nil && prior.PayloadHash != received.PayloadHash:
		return MessageResult{}, fmt.Errorf("%w: message %s was first received with another payload", ErrInboxHashMismatch, messageID)
	case prior != nil:
		return MessageResult{Outcome: prior.Outcome, TransactionID: prior.TransactionID, Duplicate: true}, nil
	}
	known, err := st.Providers().Exists(ctx, cmd.ProviderID)
	switch {
	case err != nil:
		return MessageResult{}, err
	case !known:
		return MessageResult{}, fmt.Errorf("%w: %s", ErrUnknownProvider, cmd.ProviderID)
	}
	result, err := s.submit(ctx, st, cmd, now)
	if err != nil {
		return MessageResult{}, err
	}
	completed := received
	completed.CompletedAt, completed.Outcome, completed.TransactionID = now, string(result.Transaction.Status), result.Transaction.ID
	if result.IdempotentReplay {
		completed.Outcome = OutcomeReplayed
	}
	if err := st.Inbox().Complete(ctx, completed); err != nil {
		return MessageResult{}, err
	}
	return MessageResult{Outcome: completed.Outcome, TransactionID: completed.TransactionID}, nil
}
