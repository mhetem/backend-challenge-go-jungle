package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

type transactions struct {
	q *database.Queries
}

func (r transactions) Get(ctx context.Context, id uuid.UUID) (*wager.Transaction, error) {
	return transactionFrom(r.q.GetTransaction(ctx, id))
}

func (r transactions) GetByIdempotencyKey(ctx context.Context, providerID, key string) (*wager.Transaction, error) {
	return transactionFrom(r.q.GetTransactionByIdempotencyKey(ctx, database.GetTransactionByIdempotencyKeyParams{
		ProviderID:     &providerID,
		IdempotencyKey: &key,
	}))
}

func (r transactions) GetByExternalID(ctx context.Context, providerID, externalID string) (*wager.Transaction, error) {
	return transactionFrom(r.q.GetTransactionByExternalID(ctx, database.GetTransactionByExternalIDParams{
		ProviderID:            &providerID,
		ExternalTransactionID: &externalID,
	}))
}

func (r transactions) Insert(ctx context.Context, tx *wager.Transaction) error {
	return classify(r.q.InsertTransaction(ctx, transactionParams(tx)))
}

func (r transactions) UpdateState(ctx context.Context, tx *wager.Transaction) error {
	p := transactionParams(tx)
	rows, err := r.q.UpdateTransactionState(ctx, database.UpdateTransactionStateParams{
		ID:                     p.ID,
		Status:                 p.Status,
		FailureCode:            p.FailureCode,
		ReferenceTransactionID: p.ReferenceTransactionID,
		ResultBalanceMinor:     p.ResultBalanceMinor,
		ResultWalletVersion:    p.ResultWalletVersion,
		Attempts:               p.Attempts,
		NextAttemptAt:          p.NextAttemptAt,
		ReferenceDeadlineAt:    p.ReferenceDeadlineAt,
		UpdatedAt:              p.UpdatedAt,
		CompletedAt:            p.CompletedAt,
	})
	switch {
	case err != nil:
		return classify(err)
	case rows == 0:
		return fmt.Errorf("%w: transaction %s is no longer %s", app.ErrConcurrentUpdate, p.ID, wager.PendingReference)
	}
	return nil
}

func (r transactions) WakeDependents(ctx context.Context, providerID, externalID string, walletID uuid.UUID, now time.Time) (int64, error) {
	rows, err := r.q.WakeDependents(ctx, database.WakeDependentsParams{
		ProviderID:                     &providerID,
		ReferenceExternalTransactionID: &externalID,
		WalletID:                       walletID,
		NextAttemptAt:                  &now,
	})
	return rows, classify(err)
}

func transactionParams(tx *wager.Transaction) database.InsertTransactionParams {
	s := tx.Snapshot()
	cur, _ := s.Money.Currency()
	amount, _ := s.Money.Minor()
	p := database.InsertTransactionParams{
		ID:                             s.ID,
		Origin:                         string(s.Kind.Origin()),
		Kind:                           string(s.Kind),
		Status:                         string(s.Status),
		WalletID:                       s.WalletID,
		PlayerID:                       s.PlayerID,
		Currency:                       string(cur),
		AmountMinor:                    amount,
		ProviderID:                     nullable(s.ProviderID),
		ExternalTransactionID:          nullable(s.ExternalTransactionID),
		IdempotencyKey:                 nullable(s.IdempotencyKey),
		PayloadHash:                    nullable(s.PayloadHash),
		RoundID:                        nullable(s.RoundID),
		GameID:                         nullable(s.GameID),
		ReferenceExternalTransactionID: nullable(s.ReferenceExternalTransactionID),
		ReferenceTransactionID:         nullable(s.ReferenceTransactionID),
		CorrelationID:                  s.CorrelationID,
		FailureCode:                    nullable(string(s.FailureCode)),
		Attempts:                       int32(s.Attempts),
		NextAttemptAt:                  nullableTime(s.NextAttemptAt),
		ReferenceDeadlineAt:            nullableTime(s.ReferenceDeadlineAt),
		CreatedAt:                      s.CreatedAt,
		UpdatedAt:                      s.UpdatedAt,
		CompletedAt:                    nullableTime(s.CompletedAt),
	}
	if s.Result != nil {
		balance, _ := s.Result.Balance.Minor()
		p.ResultBalanceMinor, p.ResultWalletVersion = &balance, &s.Result.WalletVersion
	}
	return p
}

func transactionFrom(row database.WagerTransaction, err error) (*wager.Transaction, error) {
	if err != nil {
		return nil, lookup(err, domain.ErrTransactionNotFound)
	}
	cur := money.Currency(row.Currency)
	amount, err := money.FromMinor(row.AmountMinor, cur)
	if err != nil {
		return nil, corrupt(err)
	}
	s := wager.Snapshot{
		ID:                             row.ID,
		WalletID:                       row.WalletID,
		PlayerID:                       row.PlayerID,
		Kind:                           wager.Kind(row.Kind),
		Money:                          amount,
		ProviderID:                     value(row.ProviderID),
		ExternalTransactionID:          value(row.ExternalTransactionID),
		IdempotencyKey:                 value(row.IdempotencyKey),
		PayloadHash:                    value(row.PayloadHash),
		RoundID:                        value(row.RoundID),
		GameID:                         value(row.GameID),
		ReferenceExternalTransactionID: value(row.ReferenceExternalTransactionID),
		ReferenceTransactionID:         value(row.ReferenceTransactionID),
		CorrelationID:                  row.CorrelationID,
		Status:                         wager.Status(row.Status),
		FailureCode:                    wager.FailureCode(value(row.FailureCode)),
		Attempts:                       int(row.Attempts),
		NextAttemptAt:                  timeValue(row.NextAttemptAt),
		ReferenceDeadlineAt:            timeValue(row.ReferenceDeadlineAt),
		CreatedAt:                      row.CreatedAt.UTC(),
		UpdatedAt:                      row.UpdatedAt.UTC(),
		CompletedAt:                    timeValue(row.CompletedAt),
	}
	if row.ResultBalanceMinor != nil {
		balance, _ := money.FromMinor(*row.ResultBalanceMinor, cur)
		s.Result = &wager.Result{Balance: balance, WalletVersion: value(row.ResultWalletVersion)}
	}
	tx, err := wager.Rehydrate(s)
	if err != nil {
		return nil, corrupt(err)
	}
	return tx, nil
}
