package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
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

func (r ledgerEntries) Post(ctx context.Context, j ledger.Journal) error {
	for _, p := range j.Postings() {
		cur, _ := p.Amount.Currency()
		amount, _ := p.Amount.Minor()
		if err := r.q.InsertLedgerPosting(ctx, database.InsertLedgerPostingParams{
			WalletID:      p.WalletID,
			TransactionID: p.TransactionID,
			Account:       string(p.Account),
			Direction:     string(p.Direction),
			AmountMinor:   amount,
			Currency:      string(cur),
			CreatedAt:     p.CreatedAt,
		}); err != nil {
			return classify(err)
		}
	}
	return nil
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

func (r ledgerEntries) Summarize(ctx context.Context, walletID uuid.UUID, cur money.Currency) (app.LedgerSummary, error) {
	row, err := r.q.SummarizeLedger(ctx, walletID)
	if err != nil {
		return app.LedgerSummary{}, classify(err)
	}
	net, err := money.ParseSigned(row.Net, string(cur))
	if err != nil {
		return app.LedgerSummary{}, corrupt(err)
	}
	postings, err := r.q.SummarizeWalletPostings(ctx, walletID)
	if err != nil {
		return app.LedgerSummary{}, classify(err)
	}
	posted, err := money.ParseSigned(postings.Net, string(cur))
	if err != nil {
		return app.LedgerSummary{}, corrupt(err)
	}
	return app.LedgerSummary{
		Entries:      row.Entries,
		FirstVersion: row.FirstVersion,
		LastVersion:  row.LastVersion,
		Net:          net,
		Posted:       posted,
	}, nil
}

func (r ledgerEntries) Totals(ctx context.Context) ([]ledger.AccountTotals, error) {
	rows, err := r.q.TrialBalance(ctx)
	if err != nil {
		return nil, classify(err)
	}
	totals := make([]ledger.AccountTotals, 0, len(rows))
	for _, row := range rows {
		debits, err := money.Parse(row.Debits, row.Currency)
		if err != nil {
			return nil, corrupt(err)
		}
		credits, err := money.Parse(row.Credits, row.Currency)
		if err != nil {
			return nil, corrupt(err)
		}
		totals = append(totals, ledger.AccountTotals{
			Account:  ledger.Account(row.Account),
			Postings: row.Postings,
			Debits:   debits,
			Credits:  credits,
		})
	}
	return totals, nil
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
