package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/failpoint"
)

type WagerResult struct {
	Transaction      wager.Snapshot
	IdempotentReplay bool
}

type WagerService struct {
	tx    TxRunner
	rules wager.Rules
	clock Clock
	ids   IDs
}

func NewWagerService(tx TxRunner, rules wager.Rules, clock Clock, ids IDs) *WagerService {
	return &WagerService{tx: tx, rules: rules, clock: clock, ids: ids}
}

func (s *WagerService) Submit(ctx context.Context, cmd SubmitWager) (WagerResult, error) {
	ctx, span := startSpan(ctx, "WagerService.Submit", commandAttributes(cmd)...)
	var result WagerResult
	err := s.tx.InTx(ctx, func(ctx context.Context, st Store) error {
		var err error
		result, err = s.submit(ctx, st, cmd, s.clock())
		return err
	})
	if err != nil {
		endSpan(span, err)
		return WagerResult{}, err
	}
	span.SetAttributes(transactionAttributes(result.Transaction)...)
	span.SetAttributes(attribute.Bool("wager.idempotent_replay", result.IdempotentReplay))
	endSpan(span, nil)
	if result.Transaction.Status == wager.PendingReference && !result.IdempotentReplay {
		failpoint.Hit(failpoint.UsecaseAfterPendingReferenceCommit)
	}
	return result, nil
}

func (s *WagerService) submit(ctx context.Context, st Store, cmd SubmitWager, now time.Time) (WagerResult, error) {
	w, err := st.Wallets().GetForUpdate(ctx, cmd.WalletID)
	if err != nil && !errors.Is(err, domain.ErrWalletNotFound) {
		return WagerResult{}, err
	}
	prior, err := previous(ctx, st, cmd)
	switch {
	case err != nil:
		return WagerResult{}, err
	case prior != nil:
		return WagerResult{Transaction: prior.Snapshot(), IdempotentReplay: true}, nil
	}
	ref, err := reference(ctx, st, cmd.ProviderID, cmd.ReferenceExternalTransactionID)
	if err != nil {
		return WagerResult{}, err
	}
	tx, err := wager.NewExternal(wager.ExternalParams{
		Payload:        cmd.Payload,
		ID:             s.ids(),
		IdempotencyKey: cmd.IdempotencyKey,
		PayloadHash:    cmd.PayloadHash,
		CorrelationID:  cmd.CorrelationID,
	}, now)
	if err != nil {
		return WagerResult{}, err
	}
	version := versionOf(w)
	out, err := s.rules.Apply(tx, w, ref, now)
	if err != nil {
		return WagerResult{}, err
	}
	if err := st.Transactions().Insert(ctx, tx); err != nil {
		return WagerResult{}, err
	}
	if err := record(ctx, st, w, version, out); err != nil {
		return WagerResult{}, err
	}
	if tx.Status().Terminal() {
		if _, err := st.Transactions().WakeDependents(ctx, cmd.ProviderID, cmd.ExternalTransactionID, cmd.WalletID, now); err != nil {
			return WagerResult{}, err
		}
	}
	return WagerResult{Transaction: tx.Snapshot()}, nil
}

func (s *WagerService) Get(ctx context.Context, id uuid.UUID) (wager.Snapshot, error) {
	return s.read(ctx, func(ctx context.Context, st Store) (*wager.Transaction, error) {
		return st.Transactions().Get(ctx, id)
	})
}

func (s *WagerService) GetByExternalID(ctx context.Context, providerID, externalID string) (wager.Snapshot, error) {
	return s.read(ctx, func(ctx context.Context, st Store) (*wager.Transaction, error) {
		return st.Transactions().GetByExternalID(ctx, providerID, externalID)
	})
}

func (s *WagerService) read(ctx context.Context, find func(context.Context, Store) (*wager.Transaction, error)) (wager.Snapshot, error) {
	var snapshot wager.Snapshot
	err := s.tx.InReadOnlySnapshot(ctx, func(ctx context.Context, st Store) error {
		tx, err := find(ctx, st)
		if err != nil {
			return err
		}
		snapshot = tx.Snapshot()
		return nil
	})
	return snapshot, err
}

func previous(ctx context.Context, st Store, cmd SubmitWager) (*wager.Transaction, error) {
	byKey, err := st.Transactions().GetByIdempotencyKey(ctx, cmd.ProviderID, cmd.IdempotencyKey)
	switch {
	case err == nil && byKey.Snapshot().PayloadHash == cmd.PayloadHash:
		return byKey, nil
	case err == nil:
		return nil, fmt.Errorf("%w: key %s belongs to transaction %s with another payload", ErrIdempotencyKeyReused, cmd.IdempotencyKey, byKey.ID())
	case !errors.Is(err, domain.ErrTransactionNotFound):
		return nil, err
	}
	byExternalID, err := st.Transactions().GetByExternalID(ctx, cmd.ProviderID, cmd.ExternalTransactionID)
	switch {
	case err == nil:
		return nil, fmt.Errorf("%w: %s is transaction %s under another key", ErrExternalIDConflict, cmd.ExternalTransactionID, byExternalID.ID())
	case errors.Is(err, domain.ErrTransactionNotFound):
		return nil, nil
	}
	return nil, err
}

func reference(ctx context.Context, st Store, providerID, externalID string) (*wager.Reference, error) {
	if externalID == "" {
		return nil, nil
	}
	tx, err := st.Transactions().GetByExternalID(ctx, providerID, externalID)
	switch {
	case errors.Is(err, domain.ErrTransactionNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	reversed, err := st.Transactions().Reversed(ctx, tx.ID())
	if err != nil {
		return nil, err
	}
	return &wager.Reference{Transaction: tx, Reversed: reversed}, nil
}

func record(ctx context.Context, st Store, w *wallet.Wallet, expectedVersion int64, out wager.Outcome) error {
	if out.Entry != nil {
		if err := st.Ledger().Insert(ctx, *out.Entry); err != nil {
			return err
		}
		if err := st.Wallets().Update(ctx, w, expectedVersion); err != nil {
			return err
		}
	}
	return st.Outbox().Insert(ctx, out.Events...)
}

func versionOf(w *wallet.Wallet) int64 {
	if w == nil {
		return 0
	}
	return w.Version()
}
