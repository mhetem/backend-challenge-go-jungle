package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type walletView struct {
	ID        uuid.UUID   `json:"id"`
	PlayerID  uuid.UUID   `json:"playerId"`
	Balance   money.Money `json:"balance"`
	Version   int64       `json:"version"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
}

func viewWallet(s wallet.Snapshot) walletView {
	return walletView{
		ID:        s.ID,
		PlayerID:  s.PlayerID,
		Balance:   s.Balance,
		Version:   s.Version,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
}

type entryView struct {
	ID            uuid.UUID   `json:"id"`
	TransactionID uuid.UUID   `json:"transactionId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
	CreatedAt     time.Time   `json:"createdAt"`
}

type ledgerView struct {
	WalletID   uuid.UUID   `json:"walletId"`
	Entries    []entryView `json:"entries"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

func viewLedger(walletID uuid.UUID, page app.LedgerPage) ledgerView {
	v := ledgerView{WalletID: walletID, Entries: make([]entryView, 0, len(page.Entries)), NextCursor: page.NextCursor}
	for _, e := range page.Entries {
		v.Entries = append(v.Entries, viewEntry(e))
	}
	return v
}

func viewEntry(e ledger.Snapshot) entryView {
	return entryView{
		ID:            e.ID,
		TransactionID: e.TransactionID,
		Direction:     string(e.Direction),
		Money:         e.Amount,
		BalanceBefore: e.BalanceBefore,
		BalanceAfter:  e.BalanceAfter,
		WalletVersion: e.WalletVersion,
		CreatedAt:     e.CreatedAt,
	}
}

type reconciliationView struct {
	WalletID           uuid.UUID   `json:"walletId"`
	StoredBalance      money.Money `json:"storedBalance"`
	CalculatedBalance  money.Money `json:"calculatedBalance"`
	PostedBalance      money.Money `json:"postedBalance"`
	Difference         money.Money `json:"difference"`
	Consistent         bool        `json:"consistent"`
	ContinuousVersions bool        `json:"continuousVersions"`
	CheckedEntries     int64       `json:"checkedEntries"`
}

func viewReconciliation(r app.Reconciliation) reconciliationView {
	return reconciliationView{
		WalletID:           r.WalletID,
		StoredBalance:      r.StoredBalance,
		CalculatedBalance:  r.CalculatedBalance,
		PostedBalance:      r.PostedBalance,
		Difference:         r.Difference,
		Consistent:         r.Consistent,
		ContinuousVersions: r.ContinuousVersions,
		CheckedEntries:     r.CheckedEntries,
	}
}

type accountView struct {
	Account       ledger.Account   `json:"account"`
	NormalBalance ledger.Direction `json:"normalBalance"`
	Postings      int64            `json:"postings"`
	Debits        money.Money      `json:"debits"`
	Credits       money.Money      `json:"credits"`
	Balance       money.Money      `json:"balance"`
}

type booksView struct {
	Currency       money.Currency `json:"currency"`
	Accounts       []accountView  `json:"accounts"`
	Debits         money.Money    `json:"debits"`
	Credits        money.Money    `json:"credits"`
	Balanced       bool           `json:"balanced"`
	Wallets        int64          `json:"wallets"`
	WalletBalances money.Money    `json:"walletBalances"`
	Consistent     bool           `json:"consistent"`
}

type trialBalanceView struct {
	Currencies []booksView `json:"currencies"`
}

func viewTrialBalance(balances []app.TrialBalance) trialBalanceView {
	v := trialBalanceView{Currencies: make([]booksView, 0, len(balances))}
	for _, tb := range balances {
		books := booksView{
			Currency:       tb.Ledger.Currency,
			Accounts:       make([]accountView, 0, len(tb.Ledger.Accounts)),
			Debits:         tb.Ledger.Debits,
			Credits:        tb.Ledger.Credits,
			Balanced:       tb.Ledger.Balanced(),
			Wallets:        tb.Wallets,
			WalletBalances: tb.WalletBalances,
			Consistent:     tb.Consistent,
		}
		for _, a := range tb.Ledger.Accounts {
			books.Accounts = append(books.Accounts, accountView{
				Account:       a.Account,
				NormalBalance: a.Account.Normal(),
				Postings:      a.Postings,
				Debits:        a.Debits,
				Credits:       a.Credits,
				Balance:       a.Balance,
			})
		}
		v.Currencies = append(v.Currencies, books)
	}
	return v
}

type resultView struct {
	TransactionID         uuid.UUID         `json:"transactionId"`
	ExternalTransactionID string            `json:"externalTransactionId"`
	Status                wager.Status      `json:"status"`
	FailureCode           wager.FailureCode `json:"failureCode,omitempty"`
	Balance               *money.Money      `json:"balance,omitempty"`
	WalletVersion         int64             `json:"walletVersion,omitzero"`
	ReferenceDeadlineAt   time.Time         `json:"referenceDeadlineAt,omitzero"`
	IdempotentReplay      bool              `json:"idempotentReplay"`
}

func viewResult(r app.WagerResult) resultView {
	s := r.Transaction
	v := resultView{
		TransactionID:         s.ID,
		ExternalTransactionID: s.ExternalTransactionID,
		Status:                s.Status,
		FailureCode:           s.FailureCode,
		ReferenceDeadlineAt:   s.ReferenceDeadlineAt,
		IdempotentReplay:      r.IdempotentReplay,
	}
	if s.Result != nil {
		v.Balance, v.WalletVersion = &s.Result.Balance, s.Result.WalletVersion
	}
	return v
}

type transactionView struct {
	TransactionID                  uuid.UUID         `json:"transactionId"`
	Origin                         wager.Origin      `json:"origin"`
	ProviderID                     string            `json:"providerId,omitempty"`
	ExternalTransactionID          string            `json:"externalTransactionId,omitempty"`
	IdempotencyKey                 string            `json:"idempotencyKey,omitempty"`
	WalletID                       uuid.UUID         `json:"walletId"`
	PlayerID                       uuid.UUID         `json:"playerId"`
	RoundID                        string            `json:"roundId,omitempty"`
	GameID                         string            `json:"gameId,omitempty"`
	Kind                           wager.Kind        `json:"kind"`
	Money                          money.Money       `json:"money"`
	ReferenceExternalTransactionID string            `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         uuid.UUID         `json:"referenceTransactionId,omitzero"`
	Status                         wager.Status      `json:"status"`
	FailureCode                    wager.FailureCode `json:"failureCode,omitempty"`
	Balance                        *money.Money      `json:"balance,omitempty"`
	WalletVersion                  int64             `json:"walletVersion,omitzero"`
	CorrelationID                  string            `json:"correlationId"`
	Attempts                       int               `json:"attempts"`
	NextAttemptAt                  time.Time         `json:"nextAttemptAt,omitzero"`
	ReferenceDeadlineAt            time.Time         `json:"referenceDeadlineAt,omitzero"`
	CreatedAt                      time.Time         `json:"createdAt"`
	UpdatedAt                      time.Time         `json:"updatedAt"`
	CompletedAt                    time.Time         `json:"completedAt,omitzero"`
}

func viewTransaction(s wager.Snapshot) transactionView {
	v := transactionView{
		TransactionID:                  s.ID,
		Origin:                         s.Kind.Origin(),
		ProviderID:                     s.ProviderID,
		ExternalTransactionID:          s.ExternalTransactionID,
		IdempotencyKey:                 s.IdempotencyKey,
		WalletID:                       s.WalletID,
		PlayerID:                       s.PlayerID,
		RoundID:                        s.RoundID,
		GameID:                         s.GameID,
		Kind:                           s.Kind,
		Money:                          s.Money,
		ReferenceExternalTransactionID: s.ReferenceExternalTransactionID,
		ReferenceTransactionID:         s.ReferenceTransactionID,
		Status:                         s.Status,
		FailureCode:                    s.FailureCode,
		CorrelationID:                  s.CorrelationID,
		Attempts:                       s.Attempts,
		NextAttemptAt:                  s.NextAttemptAt,
		ReferenceDeadlineAt:            s.ReferenceDeadlineAt,
		CreatedAt:                      s.CreatedAt,
		UpdatedAt:                      s.UpdatedAt,
		CompletedAt:                    s.CompletedAt,
	}
	if s.Result != nil {
		v.Balance, v.WalletVersion = &s.Result.Balance, s.Result.WalletVersion
	}
	return v
}
