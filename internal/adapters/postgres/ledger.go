package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

type ledgerEntries struct {
	q *database.Queries
}

func (r ledgerEntries) Insert(ctx context.Context, e ledger.Entry) error {
	s := e.Snapshot()
	cur, _ := s.Amount.Currency()
	amount, _ := s.Amount.Minor()
	before, _ := s.BalanceBefore.Minor()
	after, _ := s.BalanceAfter.Minor()
	return classify(r.q.InsertLedgerEntry(ctx, database.InsertLedgerEntryParams{
		ID:                 s.ID,
		WalletID:           s.WalletID,
		TransactionID:      s.TransactionID,
		Direction:          string(s.Direction),
		AmountMinor:        amount,
		Currency:           string(cur),
		BalanceBeforeMinor: before,
		BalanceAfterMinor:  after,
		WalletVersion:      s.WalletVersion,
		CreatedAt:          s.CreatedAt,
	}))
}

func (r ledgerEntries) Page(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) ([]ledger.Entry, error) {
	rows, err := r.q.ListLedgerEntries(ctx, database.ListLedgerEntriesParams{
		WalletID:      walletID,
		WalletVersion: afterVersion,
		Limit:         int32(limit),
	})
	if err != nil {
		return nil, classify(err)
	}
	entries := make([]ledger.Entry, 0, len(rows))
	for _, row := range rows {
		e, err := entryFrom(row)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func entryFrom(row database.LedgerEntry) (ledger.Entry, error) {
	cur := money.Currency(row.Currency)
	amount, err := money.FromMinor(row.AmountMinor, cur)
	if err != nil {
		return ledger.Entry{}, corrupt(err)
	}
	before, _ := money.FromMinor(row.BalanceBeforeMinor, cur)
	after, _ := money.FromMinor(row.BalanceAfterMinor, cur)
	e, err := ledger.New(ledger.Snapshot{
		ID:            row.ID,
		WalletID:      row.WalletID,
		TransactionID: row.TransactionID,
		Direction:     ledger.Direction(row.Direction),
		Amount:        amount,
		BalanceBefore: before,
		BalanceAfter:  after,
		WalletVersion: row.WalletVersion,
		CreatedAt:     row.CreatedAt.UTC(),
	})
	if err != nil {
		return ledger.Entry{}, corrupt(err)
	}
	return e, nil
}
