package ledger_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

func leg(t *testing.T, account ledger.Account, dir ledger.Direction, minor int64) ledger.Posting {
	return ledger.Posting{
		WalletID:      walletID,
		TransactionID: transactionID,
		Account:       account,
		Direction:     dir,
		Amount:        amount(t, minor, money.BRL),
		CreatedAt:     t0,
	}
}

func TestTransferMirrorsTheEntryAgainstTheCounterparty(t *testing.T) {
	bet, err := ledger.New(debit(t))
	if err != nil {
		t.Fatal(err)
	}
	opening := debit(t)
	opening.Direction, opening.BalanceBefore, opening.BalanceAfter, opening.WalletVersion = ledger.Credit, amount(t, 0, money.BRL), amount(t, 2500, money.BRL), 1
	credit, err := ledger.New(opening)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		entry        ledger.Entry
		counterparty ledger.Account
		want         []ledger.Posting
	}{
		{"bet", bet, ledger.GamingRevenue, []ledger.Posting{
			leg(t, ledger.PlayerBalances, ledger.Debit, 2500),
			leg(t, ledger.GamingRevenue, ledger.Credit, 2500),
		}},
		{"opening", credit, ledger.Funding, []ledger.Posting{
			leg(t, ledger.PlayerBalances, ledger.Credit, 2500),
			leg(t, ledger.Funding, ledger.Debit, 2500),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := ledger.Transfer(tt.entry, tt.counterparty)
			if err != nil || !reflect.DeepEqual(j.Postings(), tt.want) {
				t.Fatalf("Transfer = %+v, %v; want %+v", j.Postings(), err, tt.want)
			}
		})
	}
}

func TestTransferRejectsABadCounterparty(t *testing.T) {
	bet, err := ledger.New(debit(t))
	if err != nil {
		t.Fatal(err)
	}
	for counterparty, want := range map[ledger.Account]string{
		ledger.PlayerBalances: "postings: INVALID_VALUE",
		"HOUSE":               "postings[1].account: INVALID_VALUE",
	} {
		if j, err := ledger.Transfer(bet, counterparty); err == nil || err.Error() != want || j.Postings() != nil {
			t.Errorf("Transfer(%s) = %+v, %v; want error %q", counterparty, j.Postings(), err, want)
		}
	}
	if _, err := ledger.Transfer(ledger.Entry{}, ledger.GamingRevenue); err == nil {
		t.Error("Transfer of a zero entry succeeded")
	}
}

func TestNewJournalRejectsUnbalancedOrMalformedJournals(t *testing.T) {
	tests := []struct {
		name     string
		postings func() []ledger.Posting
		want     string
	}{
		{"no postings", func() []ledger.Posting { return nil }, "postings: INVALID_VALUE"},
		{"a single leg", func() []ledger.Posting {
			return []ledger.Posting{leg(t, ledger.PlayerBalances, ledger.Debit, 2500)}
		}, "postings: INVALID_VALUE"},
		{"debits above credits", func() []ledger.Posting {
			return []ledger.Posting{leg(t, ledger.PlayerBalances, ledger.Debit, 2500), leg(t, ledger.GamingRevenue, ledger.Credit, 2000)}
		}, "postings: INVALID_VALUE"},
		{"both legs on the same side", func() []ledger.Posting {
			return []ledger.Posting{leg(t, ledger.PlayerBalances, ledger.Debit, 2500), leg(t, ledger.GamingRevenue, ledger.Debit, 2500)}
		}, "postings: INVALID_VALUE"},
		{"the same account twice", func() []ledger.Posting {
			return []ledger.Posting{leg(t, ledger.PlayerBalances, ledger.Debit, 2500), leg(t, ledger.PlayerBalances, ledger.Credit, 2500)}
		}, "postings: INVALID_VALUE"},
		{"another transaction", func() []ledger.Posting {
			other := leg(t, ledger.GamingRevenue, ledger.Credit, 2500)
			other.TransactionID = uuid.New()
			return []ledger.Posting{leg(t, ledger.PlayerBalances, ledger.Debit, 2500), other}
		}, "postings: INVALID_VALUE"},
		{"another wallet", func() []ledger.Posting {
			other := leg(t, ledger.GamingRevenue, ledger.Credit, 2500)
			other.WalletID = uuid.New()
			return []ledger.Posting{leg(t, ledger.PlayerBalances, ledger.Debit, 2500), other}
		}, "postings: INVALID_VALUE"},
		{"another currency", func() []ledger.Posting {
			other := leg(t, ledger.GamingRevenue, ledger.Credit, 2500)
			other.Amount = amount(t, 2500, money.USD)
			return []ledger.Posting{leg(t, ledger.PlayerBalances, ledger.Debit, 2500), other}
		}, "postings: INVALID_VALUE"},
		{"credits that overflow", func() []ledger.Posting {
			return []ledger.Posting{
				leg(t, ledger.Funding, ledger.Debit, math.MaxInt64),
				leg(t, ledger.PlayerBalances, ledger.Credit, math.MaxInt64),
				leg(t, ledger.GamingRevenue, ledger.Credit, 1),
			}
		}, "postings: INVALID_VALUE"},
		{"invalid legs", func() []ledger.Posting {
			bad := ledger.Posting{Account: "HOUSE", Direction: "debit", Amount: amount(t, 0, money.BRL)}
			return []ledger.Posting{bad, leg(t, ledger.GamingRevenue, ledger.Credit, 2500)}
		}, "postings[0].walletId: REQUIRED\npostings[0].transactionId: REQUIRED\npostings[0].account: INVALID_VALUE\n" +
			"postings[0].direction: INVALID_VALUE\npostings[0].amount: INVALID_AMOUNT\npostings[0].createdAt: REQUIRED"},
		{"uninitialized amount", func() []ledger.Posting {
			bad := leg(t, ledger.PlayerBalances, ledger.Debit, 2500)
			bad.Amount = money.Money{}
			return []ledger.Posting{bad, leg(t, ledger.GamingRevenue, ledger.Credit, 2500)}
		}, "postings[0].amount: REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := ledger.NewJournal(tt.postings()...)
			if err == nil || err.Error() != tt.want || j.Postings() != nil {
				t.Fatalf("NewJournal = %+v, %v; want error %q", j.Postings(), err, tt.want)
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Category != domain.Invalid {
				t.Fatalf("error %v is not a domain Invalid error", err)
			}
		})
	}
}

func TestJournalPostingsAreACopy(t *testing.T) {
	postings := []ledger.Posting{leg(t, ledger.PlayerBalances, ledger.Debit, 2500), leg(t, ledger.GamingRevenue, ledger.Credit, 2500)}
	j, err := ledger.NewJournal(postings...)
	if err != nil {
		t.Fatal(err)
	}
	postings[0].Account = ledger.Funding
	got := j.Postings()
	got[1].Direction = ledger.Debit
	if again := j.Postings(); again[0].Account != ledger.PlayerBalances || again[1].Direction != ledger.Credit {
		t.Fatalf("journal changed through its inputs or outputs: %+v", again)
	}
}

func TestAccounts(t *testing.T) {
	if got := ledger.Chart(); !reflect.DeepEqual(got, []ledger.Account{ledger.Funding, ledger.PlayerBalances, ledger.GamingRevenue}) {
		t.Fatalf("Chart() = %v", got)
	}
	for account, normal := range map[ledger.Account]ledger.Direction{
		ledger.Funding:        ledger.Debit,
		ledger.PlayerBalances: ledger.Credit,
		ledger.GamingRevenue:  ledger.Credit,
	} {
		if !account.Valid() || account.Normal() != normal {
			t.Errorf("%s: valid %v, normal %s; want valid with a %s normal balance", account, account.Valid(), account.Normal(), normal)
		}
	}
	if ledger.Account("HOUSE").Valid() || ledger.Account("").Valid() {
		t.Error("an account outside the chart is valid")
	}
	if ledger.Debit.Opposite() != ledger.Credit || ledger.Credit.Opposite() != ledger.Debit || ledger.Direction("x").Opposite() != "x" {
		t.Error("Opposite does not swap the two directions")
	}
}

func TestNewTrialBalance(t *testing.T) {
	brl := func(minor int64) money.Money { return amount(t, minor, money.BRL) }
	tb, err := ledger.NewTrialBalance(money.BRL, []ledger.AccountTotals{
		{Account: ledger.PlayerBalances, Postings: 4, Debits: brl(4000), Credits: brl(11500)},
		{Account: ledger.GamingRevenue, Postings: 3, Debits: brl(1500), Credits: brl(4000)},
		{Account: ledger.Funding, Postings: 1, Debits: brl(10000), Credits: brl(0)},
	})
	want := ledger.TrialBalance{
		Currency: money.BRL,
		Accounts: []ledger.AccountTotals{
			{Account: ledger.Funding, Postings: 1, Debits: brl(10000), Credits: brl(0), Balance: brl(10000)},
			{Account: ledger.PlayerBalances, Postings: 4, Debits: brl(4000), Credits: brl(11500), Balance: brl(7500)},
			{Account: ledger.GamingRevenue, Postings: 3, Debits: brl(1500), Credits: brl(4000), Balance: brl(2500)},
		},
		Debits:  brl(15500),
		Credits: brl(15500),
	}
	if err != nil || !reflect.DeepEqual(tb, want) || !tb.Balanced() {
		t.Fatalf("NewTrialBalance = %+v, %v; want balanced %+v", tb, err, want)
	}
	if got := tb.Account(ledger.PlayerBalances).Balance; got != brl(7500) {
		t.Fatalf("player balances = %s; want 75.00", got)
	}
	if got := tb.Account("HOUSE"); !reflect.DeepEqual(got, ledger.AccountTotals{}) {
		t.Fatalf("unknown account = %+v; want the zero value", got)
	}

	lost, err := ledger.NewTrialBalance(money.BRL, []ledger.AccountTotals{
		{Account: ledger.Funding, Postings: 1, Debits: brl(10000), Credits: brl(0)},
		{Account: ledger.PlayerBalances, Postings: 2, Debits: brl(2500), Credits: brl(10000)},
		{Account: ledger.GamingRevenue, Postings: 1, Debits: brl(1000), Credits: brl(0)},
	})
	if err != nil || lost.Balanced() || lost.Account(ledger.GamingRevenue).Balance != brl(-1000) {
		t.Fatalf("unbalanced books = %+v, %v; want unbalanced with gaming revenue at -10.00", lost, err)
	}

	empty, err := ledger.NewTrialBalance(money.EUR, nil)
	if err != nil || !empty.Balanced() || len(empty.Accounts) != 3 || empty.Debits != amount(t, 0, money.EUR) {
		t.Fatalf("empty books = %+v, %v; want the whole chart at zero", empty, err)
	}
}

func TestNewTrialBalanceRejectsBadTotals(t *testing.T) {
	brl := func(minor int64) money.Money { return amount(t, minor, money.BRL) }
	tests := []struct {
		name   string
		cur    money.Currency
		totals []ledger.AccountTotals
	}{
		{"unknown currency", "XYZ", nil},
		{"account outside the chart", money.BRL, []ledger.AccountTotals{{Account: "HOUSE", Debits: brl(1), Credits: brl(0)}}},
		{"negative postings", money.BRL, []ledger.AccountTotals{{Account: ledger.Funding, Postings: -1, Debits: brl(0), Credits: brl(0)}}},
		{"another currency", money.BRL, []ledger.AccountTotals{
			{Account: ledger.Funding, Postings: 1, Debits: amount(t, 100, money.USD), Credits: amount(t, 0, money.USD)},
		}},
		{"uninitialized totals", money.BRL, []ledger.AccountTotals{{Account: ledger.Funding, Postings: 1}}},
		{"overflowing debits", money.BRL, []ledger.AccountTotals{
			{Account: ledger.Funding, Postings: 1, Debits: brl(math.MaxInt64), Credits: brl(0)},
			{Account: ledger.PlayerBalances, Postings: 1, Debits: brl(1), Credits: brl(0)},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tb, err := ledger.NewTrialBalance(tt.cur, tt.totals); err == nil {
				t.Fatalf("NewTrialBalance = %+v; want an error", tb)
			}
		})
	}
}
