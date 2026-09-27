package postgres

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
)

const maxLastError = 1000

type outbox struct {
	q *database.Queries
}

func (r outbox) Insert(ctx context.Context, evs ...events.Event) error {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	traceParent := nullable(carrier.Get("traceparent"))
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
			TraceParent:   traceParent,
		}); err != nil {
			return classify(err)
		}
	}
	return nil
}

func (r outbox) Claim(ctx context.Context, owner string, now, until time.Time, limit int) ([]app.OutboxMessage, error) {
	rows, err := r.q.ClaimOutboxEvents(ctx, database.ClaimOutboxEventsParams{
		ClaimedBy:     &owner,
		ClaimedUntil:  &until,
		NextAttemptAt: now,
		Limit:         int32(limit),
	})
	if err != nil {
		return nil, classify(err)
	}
	msgs := make([]app.OutboxMessage, len(rows))
	for i, row := range rows {
		msgs[i] = app.OutboxMessage{
			ID:            row.ID,
			Seq:           value(row.Seq),
			PartitionKey:  row.PartitionKey,
			EventType:     row.EventType,
			EventVersion:  int(row.EventVersion),
			CorrelationID: row.CorrelationID,
			Payload:       row.Payload,
			OccurredAt:    row.OccurredAt.UTC(),
			Attempts:      int(row.Attempts),
			TraceParent:   value(row.TraceParent),
		}
	}
	slices.SortFunc(msgs, func(a, b app.OutboxMessage) int { return cmp.Compare(a.Seq, b.Seq) })
	return msgs, nil
}

func (r outbox) MarkPublished(ctx context.Context, id uuid.UUID, owner string, now time.Time) (bool, error) {
	rows, err := r.q.MarkOutboxEventPublished(ctx, database.MarkOutboxEventPublishedParams{
		ID:          id,
		ClaimedBy:   &owner,
		PublishedAt: &now,
	})
	return rows == 1, classify(err)
}

func (r outbox) Release(ctx context.Context, id uuid.UUID, owner string, next time.Time, lastError string) (bool, error) {
	if len(lastError) > maxLastError {
		lastError = strings.ToValidUTF8(lastError[:maxLastError], "")
	}
	rows, err := r.q.ReleaseOutboxEvent(ctx, database.ReleaseOutboxEventParams{
		ID:            id,
		ClaimedBy:     &owner,
		NextAttemptAt: next,
		LastError:     &lastError,
	})
	return rows == 1, classify(err)
}

func (r outbox) Backlog(ctx context.Context) (int64, time.Time, error) {
	pending, err := r.q.CountPendingOutboxEvents(ctx)
	if err != nil || pending == 0 {
		return 0, time.Time{}, classify(err)
	}
	oldest, err := r.q.OldestPendingOutboxEvent(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, time.Time{}, nil
	}
	if err != nil {
		return 0, time.Time{}, classify(err)
	}
	return pending, oldest.UTC(), nil
}
