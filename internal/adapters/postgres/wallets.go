package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

type wallets struct {
	q *database.Queries
}

func (r wallets) Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return walletFrom(r.q.GetWallet(ctx, id))
}

func (r wallets) GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return walletFrom(r.q.GetWalletForUpdate(ctx, id))
}

func (r wallets) GetByPlayer(ctx context.Context, playerID uuid.UUID, cur money.Currency) (*wallet.Wallet, error) {
	return walletFrom(r.q.GetWalletByPlayer(ctx, database.GetWalletByPlayerParams{
		PlayerID: playerID,
		Currency: string(cur),
	}))
}

func (r wallets) Insert(ctx context.Context, w *wallet.Wallet) error {
	s := w.Snapshot()
	cur, _ := s.Balance.Currency()
	balance, _ := s.Balance.Minor()
	return classify(r.q.InsertWallet(ctx, database.InsertWalletParams{
		ID:           s.ID,
		PlayerID:     s.PlayerID,
		Currency:     string(cur),
		BalanceMinor: balance,
		Version:      s.Version,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
	}))
}

func (r wallets) Update(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error {
	s := w.Snapshot()
	balance, _ := s.Balance.Minor()
	rows, err := r.q.UpdateWalletBalance(ctx, database.UpdateWalletBalanceParams{
		ID:           s.ID,
		Version:      expectedVersion,
		BalanceMinor: balance,
		UpdatedAt:    s.UpdatedAt,
	})
	switch {
	case err != nil:
		return classify(err)
	case rows == 0:
		return fmt.Errorf("%w: wallet %s is not at version %d", app.ErrConcurrentUpdate, s.ID, expectedVersion)
	}
	return nil
}

func walletFrom(row database.Wallet, err error) (*wallet.Wallet, error) {
	if err != nil {
		return nil, lookup(err, domain.ErrWalletNotFound)
	}
	balance, err := money.FromMinor(row.BalanceMinor, money.Currency(row.Currency))
	if err != nil {
		return nil, corrupt(err)
	}
	w, err := wallet.Rehydrate(wallet.Snapshot{
		ID:        row.ID,
		PlayerID:  row.PlayerID,
		Balance:   balance,
		Version:   row.Version,
		CreatedAt: row.CreatedAt.UTC(),
		UpdatedAt: row.UpdatedAt.UTC(),
	})
	if err != nil {
		return nil, corrupt(err)
	}
	return w, nil
}
