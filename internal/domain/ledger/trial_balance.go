package ledger

import (
	"fmt"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

func Chart() []Account {
	return []Account{Funding, PlayerBalances, GamingRevenue}
}

type AccountTotals struct {
	Account  Account
	Postings int64
	Debits   money.Money
	Credits  money.Money
	Balance  money.Money
}

type TrialBalance struct {
	Currency money.Currency
	Accounts []AccountTotals
	Debits   money.Money
	Credits  money.Money
}

func NewTrialBalance(cur money.Currency, totals []AccountTotals) (TrialBalance, error) {
	zero, err := money.Zero(cur)
	if err != nil {
		return TrialBalance{}, err
	}
	byAccount := map[Account]AccountTotals{}
	for _, t := range totals {
		if !t.Account.Valid() || t.Postings < 0 {
			return TrialBalance{}, fmt.Errorf("%w: account %q with %d postings", domain.ErrInvalidValue, t.Account, t.Postings)
		}
		byAccount[t.Account] = t
	}
	tb := TrialBalance{Currency: cur, Debits: zero, Credits: zero}
	for _, account := range Chart() {
		t, ok := byAccount[account]
		if !ok {
			t = AccountTotals{Account: account, Debits: zero, Credits: zero}
		}
		if err := tb.add(t); err != nil {
			return TrialBalance{}, fmt.Errorf("%s: %w", account, err)
		}
	}
	return tb, nil
}

func (tb *TrialBalance) add(t AccountTotals) error {
	var err error
	if t.Account.Normal() == Debit {
		t.Balance, err = t.Debits.Sub(t.Credits)
	} else {
		t.Balance, err = t.Credits.Sub(t.Debits)
	}
	if err != nil {
		return err
	}
	if tb.Debits, err = tb.Debits.Add(t.Debits); err != nil {
		return err
	}
	if tb.Credits, err = tb.Credits.Add(t.Credits); err != nil {
		return err
	}
	tb.Accounts = append(tb.Accounts, t)
	return nil
}

func (tb TrialBalance) Balanced() bool {
	return tb.Debits == tb.Credits
}

func (tb TrialBalance) Account(a Account) AccountTotals {
	for _, t := range tb.Accounts {
		if t.Account == a {
			return t
		}
	}
	return AccountTotals{}
}
