package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
)

type outbox struct {
	q *database.Queries
}

func (r outbox) Insert(ctx context.Context, evs ...events.Event) error {
	for _, e := range evs {
		h := e.EventHeader()
		payload, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("%w: encode %s %s: %w", app.ErrPermanent, h.EventType, h.EventID, err)
		}
		if err := r.q.InsertOutboxEvent(ctx, database.InsertOutboxEventParams{
			ID:            h.EventID,
			AggregateType: h.EventType.AggregateType(),
			AggregateID:   h.AggregateID,
			PartitionKey:  e.PartitionKey(),
			EventType:     string(h.EventType),
			EventVersion:  int32(h.Version),
			CorrelationID: h.CorrelationID,
			CausationID:   nullable(h.CausationID),
			Payload:       payload,
			OccurredAt:    h.OccurredAt,
			NextAttemptAt: h.OccurredAt,
		}); err != nil {
			return classify(err)
		}
	}
	return nil
}
