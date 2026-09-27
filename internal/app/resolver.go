package app

import (
	"context"
	"errors"
	"time"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

type Resolution struct {
	Status  wager.Status
	Skipped bool
}

func (s *WagerService) Due(ctx context.Context, limit int) ([]DueTransaction, error) {
	var due []DueTransaction
	err := s.tx.InReadOnlySnapshot(ctx, func(ctx context.Context, st Store) error {
		var err error
		due, err = st.Transactions().Due(ctx, s.clock(), limit)
		return err
	})
	return due, err
}

func (s *WagerService) Resolve(ctx context.Context, due DueTransaction) (Resolution, error) {
	var res Resolution
	err := s.tx.InTx(ctx, func(ctx context.Context, st Store) error {
		var err error
		res, err = s.resolve(ctx, st, due, s.clock())
		return err
	})
	if err != nil {
		return Resolution{}, err
	}
	return res, nil
}

func (s *WagerService) resolve(ctx context.Context, st Store, due DueTransaction, now time.Time) (Resolution, error) {
	w, err := st.Wallets().GetForUpdate(ctx, due.WalletID)
	if err != nil && !errors.Is(err, domain.ErrWalletNotFound) {
		return Resolution{}, err
	}
	tx, err := st.Transactions().GetForUpdate(ctx, due.ID)
	if err != nil {
		return Resolution{}, err
	}
	snap := tx.Snapshot()
	if snap.Status != wager.PendingReference || snap.WalletID != due.WalletID || snap.NextAttemptAt.After(now) {
		return Resolution{Status: snap.Status, Skipped: true}, nil
	}
	ref, err := reference(ctx, st, snap.ProviderID, snap.ReferenceExternalTransactionID)
	if err != nil {
		return Resolution{}, err
	}
	version := versionOf(w)
	out, err := s.rules.Apply(tx, w, ref, now)
	if err != nil {
		return Resolution{}, err
	}
	if err := st.Transactions().UpdateState(ctx, tx); err != nil {
		return Resolution{}, err
	}
	if err := record(ctx, st, w, version, out); err != nil {
		return Resolution{}, err
	}
	if tx.Status().Terminal() {
		if _, err := st.Transactions().WakeDependents(ctx, snap.ProviderID, snap.ExternalTransactionID, snap.WalletID, now); err != nil {
			return Resolution{}, err
		}
	}
	return Resolution{Status: tx.Status()}, nil
}

func (s *WagerService) Fail(ctx context.Context, due DueTransaction) (bool, error) {
	var failed bool
	err := s.tx.InTx(ctx, func(ctx context.Context, st Store) error {
		var err error
		failed, err = st.Transactions().MarkFailed(ctx, due.ID, s.clock())
		return err
	})
	return failed, err
}
