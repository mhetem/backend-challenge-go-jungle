package postgres

import (
	"context"
	"fmt"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
)

type inbox struct {
	q *database.Queries
}

func (r inbox) Receive(ctx context.Context, m app.InboxMessage) (*app.InboxMessage, error) {
	rows, err := r.q.InsertInboxMessage(ctx, database.InsertInboxMessageParams{
		ConsumerName: m.Consumer,
		MessageID:    m.MessageID,
		PayloadHash:  m.PayloadHash,
		ReceivedAt:   m.ReceivedAt,
	})
	switch {
	case err != nil:
		return nil, classify(err)
	case rows == 1:
		return nil, nil
	}
	row, err := r.q.GetInboxMessage(ctx, database.GetInboxMessageParams{
		ConsumerName: m.Consumer,
		MessageID:    m.MessageID,
	})
	if err != nil {
		return nil, classify(err)
	}
	return &app.InboxMessage{
		Consumer:      row.ConsumerName,
		MessageID:     row.MessageID,
		PayloadHash:   row.PayloadHash,
		ReceivedAt:    row.ReceivedAt.UTC(),
		CompletedAt:   timeValue(row.CompletedAt),
		Outcome:       value(row.Outcome),
		TransactionID: value(row.TransactionID),
	}, nil
}

func (r inbox) Complete(ctx context.Context, m app.InboxMessage) error {
	rows, err := r.q.CompleteInboxMessage(ctx, database.CompleteInboxMessageParams{
		ConsumerName:  m.Consumer,
		MessageID:     m.MessageID,
		CompletedAt:   nullableTime(m.CompletedAt),
		Outcome:       nullable(m.Outcome),
		TransactionID: nullable(m.TransactionID),
	})
	switch {
	case err != nil:
		return classify(err)
	case rows == 0:
		return fmt.Errorf("%w: inbox message %s/%s is not open", app.ErrPermanent, m.Consumer, m.MessageID)
	}
	return nil
}
