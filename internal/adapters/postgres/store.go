package postgres

import (
	"time"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
)

type store struct {
	q *database.Queries
}

func (s store) Wallets() app.Wallets {
	return wallets(s)
}

func (s store) Transactions() app.Transactions {
	return transactions(s)
}

func (s store) Ledger() app.Ledger {
	return ledgerEntries(s)
}

func (s store) Outbox() app.Outbox {
	return outbox(s)
}

func (s store) Inbox() app.Inbox {
	return inbox(s)
}

func (s store) Providers() app.Providers {
	return providers(s)
}

func nullable[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func value[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func timeValue(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.UTC()
}
