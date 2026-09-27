package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

type booksLedger struct {
	app.Ledger
	totals []ledger.AccountTotals
}

func (l booksLedger) Totals(context.Context) ([]ledger.AccountTotals, error) {
	return l.totals, nil
}

type booksWallets struct {
	app.Wallets
	totals []app.WalletTotals
}

func (w booksWallets) Totals(context.Context) ([]app.WalletTotals, error) {
	return w.totals, nil
}

type booksStore struct {
	app.Store
	ledger  booksLedger
	wallets booksWallets
}

func (s booksStore) Ledger() app.Ledger   { return s.ledger }
func (s booksStore) Wallets() app.Wallets { return s.wallets }

type storeRunner struct {
	st app.Store
}

func (r storeRunner) InTx(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return fn(ctx, r.st)
}

func (r storeRunner) InReadOnlySnapshot(ctx context.Context, fn func(context.Context, app.Store) error) error {
	return fn(ctx, r.st)
}

type countingMetrics struct {
	diverged int
}

func (m *countingMetrics) ReconciliationDiverged() {
	m.diverged++
}

func minorUnits(t *testing.T, minor int64, cur money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(minor, cur)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTrialBalanceChecksTheBooksOfEachCurrency(t *testing.T) {
	brl := func(minor int64) money.Money { return minorUnits(t, minor, money.BRL) }
	usd := func(minor int64) money.Money { return minorUnits(t, minor, money.USD) }
	eur := func(minor int64) money.Money { return minorUnits(t, minor, money.EUR) }
	st := booksStore{
		ledger: booksLedger{totals: []ledger.AccountTotals{
			{Account: ledger.Funding, Postings: 1, Debits: usd(5000), Credits: usd(0)},
			{Account: ledger.PlayerBalances, Postings: 1, Debits: usd(0), Credits: usd(5000)},
			{Account: ledger.Funding, Postings: 1, Debits: brl(10000), Credits: brl(0)},
			{Account: ledger.GamingRevenue, Postings: 2, Debits: brl(1000), Credits: brl(3000)},
			{Account: ledger.PlayerBalances, Postings: 3, Debits: brl(3000), Credits: brl(11000)},
		}},
		wallets: booksWallets{totals: []app.WalletTotals{
			{Wallets: 2, Balance: brl(8000)},
			{Wallets: 1, Balance: eur(0)},
			{Wallets: 1, Balance: usd(4000)},
		}},
	}
	metrics := &countingMetrics{}
	var logs bytes.Buffer
	s := app.NewWalletService(storeRunner{st}, app.SystemClock, app.NewID, slog.New(slog.NewJSONHandler(&logs, nil)), metrics)
	got, err := s.TrialBalance(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []app.TrialBalance{
		{
			Ledger: ledger.TrialBalance{
				Currency: money.BRL,
				Accounts: []ledger.AccountTotals{
					{Account: ledger.Funding, Postings: 1, Debits: brl(10000), Credits: brl(0), Balance: brl(10000)},
					{Account: ledger.PlayerBalances, Postings: 3, Debits: brl(3000), Credits: brl(11000), Balance: brl(8000)},
					{Account: ledger.GamingRevenue, Postings: 2, Debits: brl(1000), Credits: brl(3000), Balance: brl(2000)},
				},
				Debits:  brl(14000),
				Credits: brl(14000),
			},
			Wallets:        2,
			WalletBalances: brl(8000),
			Consistent:     true,
		},
		{
			Ledger: ledger.TrialBalance{
				Currency: money.EUR,
				Accounts: []ledger.AccountTotals{
					{Account: ledger.Funding, Debits: eur(0), Credits: eur(0), Balance: eur(0)},
					{Account: ledger.PlayerBalances, Debits: eur(0), Credits: eur(0), Balance: eur(0)},
					{Account: ledger.GamingRevenue, Debits: eur(0), Credits: eur(0), Balance: eur(0)},
				},
				Debits:  eur(0),
				Credits: eur(0),
			},
			Wallets:        1,
			WalletBalances: eur(0),
			Consistent:     true,
		},
		{
			Ledger: ledger.TrialBalance{
				Currency: money.USD,
				Accounts: []ledger.AccountTotals{
					{Account: ledger.Funding, Postings: 1, Debits: usd(5000), Credits: usd(0), Balance: usd(5000)},
					{Account: ledger.PlayerBalances, Postings: 1, Debits: usd(0), Credits: usd(5000), Balance: usd(5000)},
					{Account: ledger.GamingRevenue, Debits: usd(0), Credits: usd(0), Balance: usd(0)},
				},
				Debits:  usd(5000),
				Credits: usd(5000),
			},
			Wallets:        1,
			WalletBalances: usd(4000),
			Consistent:     false,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trial balance =\n%+v\nwant\n%+v", got, want)
	}
	if metrics.diverged != 1 || strings.Count(logs.String(), `"level":"WARN"`) != 1 || !strings.Contains(logs.String(), `"currency":"USD"`) {
		t.Fatalf("%d divergences counted, logs %q; want the USD books reported once", metrics.diverged, logs.String())
	}
}

func TestLedgerRejectsBadPaging(t *testing.T) {
	s := app.NewWalletService(nil, app.SystemClock, app.NewID, slog.New(slog.DiscardHandler), nil)
	tests := []struct {
		name   string
		cursor string
		limit  int
		want   string
	}{
		{"cursor that is not base64url", "!!", 0, "cursor: INVALID_VALUE"},
		{"padded cursor", "MQ==", 0, "cursor: INVALID_VALUE"},
		{"cursor at version zero", "MA", 0, "cursor: INVALID_VALUE"},
		{"negative cursor", "LTE", 0, "cursor: INVALID_VALUE"},
		{"cursor with a leading zero", "MDE", 0, "cursor: INVALID_VALUE"},
		{"cursor with a sign", "KzE", 0, "cursor: INVALID_VALUE"},
		{"cursor with a space", "IDE", 0, "cursor: INVALID_VALUE"},
		{"negative limit", "", -1, "limit: INVALID_VALUE"},
		{"limit above the maximum", "", app.MaxLedgerLimit + 1, "limit: INVALID_VALUE"},
		{"both", "!!", app.MaxLedgerLimit + 1, "cursor: INVALID_VALUE\nlimit: INVALID_VALUE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := s.Ledger(t.Context(), uuid.New(), tt.cursor, tt.limit)
			if err == nil || err.Error() != tt.want || page.Entries != nil || page.NextCursor != "" {
				t.Fatalf("Ledger = %+v, %v; want error %q", page, err, tt.want)
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Category != domain.Invalid {
				t.Fatalf("error %v is not a domain Invalid error", err)
			}
		})
	}
}

func TestWalletExistsError(t *testing.T) {
	id := uuid.MustParse(walletID)
	var err error = &app.WalletExistsError{WalletID: id}
	var exists *app.WalletExistsError
	var de *domain.Error
	if !errors.Is(err, domain.ErrWalletExists) || !errors.As(err, &exists) || exists.WalletID != id ||
		!errors.As(err, &de) || de.Category != domain.Conflict {
		t.Fatalf("%v does not classify as %v carrying %s", err, domain.ErrWalletExists, id)
	}
	if want := "WALLET_ALREADY_EXISTS: wallet " + walletID; err.Error() != want {
		t.Fatalf("Error() = %q; want %q", err.Error(), want)
	}
}
