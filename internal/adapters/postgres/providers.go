package postgres

import (
	"context"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
)

type providers struct {
	q *database.Queries
}

func (r providers) Exists(ctx context.Context, id string) (bool, error) {
	exists, err := r.q.ProviderExists(ctx, id)
	return exists, classify(err)
}
