//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres/database"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
)

func TestWalletsRoundTrip(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	empty := openWallet(t, r, 0)
	funded := openWallet(t, r, 10000)
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		for _, want := range []*wallet.Wallet{empty.Wallet, funded.Wallet} {
			got, err := s.Wallets().Get(ctx, want.ID())
			if err != nil {
				return err
			}
			requireEqual(t, got.Snapshot(), want.Snapshot())
			locked, err := s.Wallets().GetForUpdate(ctx, want.ID())
			if err != nil {
				return err
			}
			requireEqual(t, locked.Snapshot(), want.Snapshot())
		}
		byPlayer, err := s.Wallets().GetByPlayer(ctx, funded.Wallet.PlayerID(), money.BRL)
		if err != nil {
			return err
		}
		requireEqual(t, byPlayer.Snapshot(), funded.Wallet.Snapshot())
		for _, get := range []func(context.Context, uuid.UUID) (*wallet.Wallet, error){s.Wallets().Get, s.Wallets().GetForUpdate} {
			if _, err := get(ctx, uuid.New()); !errors.Is(err, domain.ErrWalletNotFound) {
				t.Fatalf("missing wallet: err = %v; want %v", err, domain.ErrWalletNotFound)
			}
		}
		if _, err := s.Wallets().GetByPlayer(ctx, funded.Wallet.PlayerID(), money.USD); !errors.Is(err, domain.ErrWalletNotFound) {
			t.Fatalf("player's USD wallet: err = %v; want %v", err, domain.ErrWalletNotFound)
		}
		return nil
	}))

	duplicate, _, err := wallet.New(uuid.New(), funded.Wallet.PlayerID(), brl(t, 0), t0)
	must(t, err)
	err = r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		return s.Wallets().Insert(ctx, duplicate)
	})
	if !errors.Is(err, domain.ErrWalletExists) {
		t.Fatalf("second BRL wallet for the player: err = %v; want %v", err, domain.ErrWalletExists)
	}
}

func TestWalletUpdateIsVersionConditional(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	opened := openWallet(t, r, 10000)
	bet, out := submit(t, r, payload(t, opened.Wallet, wager.Bet, "bet-1", 2500), t0.Add(time.Minute))
	if bet.Status() != wager.Processed || out.Entry == nil {
		t.Fatalf("bet = %s; want PROCESSED with a ledger entry", bet.Status())
	}
	want := wallet.Snapshot{
		ID:        opened.Wallet.ID(),
		PlayerID:  opened.Wallet.PlayerID(),
		Balance:   brl(t, 7500),
		Version:   2,
		CreatedAt: t0,
		UpdatedAt: t0.Add(time.Minute),
	}
	attempts := 0
	err := r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		attempts++
		w, err := s.Wallets().GetForUpdate(ctx, opened.Wallet.ID())
		if err != nil {
			return err
		}
		requireEqual(t, w.Snapshot(), want)
		if _, err := w.Debit(uuid.New(), brl(t, 100), t0.Add(2*time.Minute)); err != nil {
			return err
		}
		return s.Wallets().Update(ctx, w, 1)
	})
	if !errors.Is(err, app.ErrConcurrentUpdate) || attempts != 3 {
		t.Fatalf("stale update: err = %v after %d attempts; want %v after 3", err, attempts, app.ErrConcurrentUpdate)
	}
	requireEqual(t, getWallet(t, r, opened.Wallet.ID()).Snapshot(), want)
}

func TestTransactionsRoundTrip(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	walletID, playerID := uuid.New(), uuid.New()
	snapshot := func(kind wager.Kind, extID string, minor int64) wager.Snapshot {
		return wager.Snapshot{
			ID:                    uuid.New(),
			WalletID:              walletID,
			PlayerID:              playerID,
			Kind:                  kind,
			Money:                 brl(t, minor),
			ProviderID:            "provider-a",
			ExternalTransactionID: extID,
			IdempotencyKey:        "provider-a:" + extID,
			PayloadHash:           hash(extID),
			RoundID:               "round-1",
			GameID:                "game-1",
			CorrelationID:         "corr-" + extID,
			CreatedAt:             t0,
			UpdatedAt:             t0,
		}
	}

	processed := snapshot(wager.Bet, "bet-1", 2500)
	processed.Status, processed.CompletedAt = wager.Processed, t0
	processed.Result = &wager.Result{Balance: brl(t, 7500), WalletVersion: 2}

	loss := snapshot(wager.Loss, "loss-1", 0)
	loss.Status, loss.CompletedAt = wager.Processed, t0
	loss.Result = &wager.Result{Balance: brl(t, 7500), WalletVersion: 2}

	mismatch := snapshot(wager.Win, "win-1", 1000)
	mismatch.ReferenceExternalTransactionID, mismatch.ReferenceTransactionID = "bet-1", processed.ID
	mismatch.Status, mismatch.FailureCode, mismatch.CompletedAt = wager.Rejected, wager.ReferenceMismatch, t0
	mismatch.Result = &wager.Result{Balance: brl(t, 7500), WalletVersion: 2}

	missing := snapshot(wager.Bet, "bet-2", 500)
	missing.Status, missing.FailureCode, missing.CompletedAt = wager.Rejected, wager.WalletNotFound, t0

	waiting := snapshot(wager.Refund, "refund-1", 2500)
	waiting.ReferenceExternalTransactionID = "bet-0"
	waiting.Status, waiting.Attempts = wager.PendingReference, 1
	waiting.NextAttemptAt, waiting.ReferenceDeadlineAt = t0.Add(time.Second), t0.Add(15*time.Minute)

	opening, err := wager.NewOpening(walletID, playerID, brl(t, 10000), "corr-open", t0)
	must(t, err)
	txs := []*wager.Transaction{
		opening,
		rehydrate(t, processed),
		rehydrate(t, loss),
		rehydrate(t, mismatch),
		rehydrate(t, missing),
		rehydrate(t, waiting),
	}
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		for _, tx := range txs {
			if err := s.Transactions().Insert(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	}))

	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		for _, tx := range txs {
			want := tx.Snapshot()
			got, err := s.Transactions().Get(ctx, want.ID)
			if err != nil {
				return err
			}
			requireEqual(t, got.Snapshot(), want)
			if want.Kind == wager.Opening {
				continue
			}
			byKey, err := s.Transactions().GetByIdempotencyKey(ctx, want.ProviderID, want.IdempotencyKey)
			if err != nil {
				return err
			}
			requireEqual(t, byKey.Snapshot(), want)
			byExternalID, err := s.Transactions().GetByExternalID(ctx, want.ProviderID, want.ExternalTransactionID)
			if err != nil {
				return err
			}
			requireEqual(t, byExternalID.Snapshot(), want)
		}
		lookups := map[string]func() (*wager.Transaction, error){
			"unknown id": func() (*wager.Transaction, error) {
				return s.Transactions().Get(ctx, uuid.New())
			},
			"key under another provider": func() (*wager.Transaction, error) {
				return s.Transactions().GetByIdempotencyKey(ctx, "provider-b", "provider-a:bet-1")
			},
			"unknown external id": func() (*wager.Transaction, error) {
				return s.Transactions().GetByExternalID(ctx, "provider-a", "bet-0")
			},
		}
		for name, lookup := range lookups {
			if _, err := lookup(); !errors.Is(err, domain.ErrTransactionNotFound) {
				t.Fatalf("%s: err = %v; want %v", name, err, domain.ErrTransactionNotFound)
			}
		}
		return nil
	}))
}

func TestTransactionStateUpdates(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	opened := openWallet(t, r, 10000)
	refund := payload(t, opened.Wallet, wager.Refund, "refund-1", 2500)
	refund.ReferenceExternalTransactionID = "bet-0"
	waiting, _ := submit(t, r, refund, t0)
	if waiting.Status() != wager.PendingReference {
		t.Fatalf("refund without its bet = %s; want %s", waiting.Status(), wager.PendingReference)
	}
	update := func(tx *wager.Transaction) error {
		return r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
			return s.Transactions().UpdateState(ctx, tx)
		})
	}

	rescheduled := getTransaction(t, r, waiting.ID())
	must(t, rescheduled.Reschedule(t0.Add(3*time.Second), t0.Add(2*time.Second)))
	must(t, update(rescheduled))
	requireEqual(t, getTransaction(t, r, waiting.ID()).Snapshot(), rescheduled.Snapshot())

	rejected := getTransaction(t, r, waiting.ID())
	must(t, rejected.Reject(wager.ReferenceNotFound, nil, t0.Add(time.Hour)))
	must(t, update(rejected))
	requireEqual(t, getTransaction(t, r, waiting.ID()).Snapshot(), rejected.Snapshot())

	if err := update(rejected); !errors.Is(err, app.ErrConcurrentUpdate) {
		t.Fatalf("update of a terminal transaction: err = %v; want %v", err, app.ErrConcurrentUpdate)
	}
}

func TestPendingReferenceWakesAndResumes(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	ctx := t.Context()
	a := openWallet(t, r, 10000).Wallet
	b := openWallet(t, r, 10000).Wallet

	rollback := payload(t, a, wager.Rollback, "rollback-1", 2500)
	rollback.ReferenceExternalTransactionID = "bet-1"
	waiting, _ := submit(t, r, rollback, t0)
	other := payload(t, b, wager.Rollback, "rollback-2", 2500)
	other.ReferenceExternalTransactionID = "bet-1"
	elsewhere, _ := submit(t, r, other, t0)
	for _, tx := range []*wager.Transaction{waiting, elsewhere} {
		if tx.Status() != wager.PendingReference {
			t.Fatalf("rollback %s = %s; want %s", tx.ID(), tx.Status(), wager.PendingReference)
		}
	}
	bet, _ := submit(t, r, payload(t, a, wager.Bet, "bet-1", 2500), t0)

	var woken []int64
	for range 2 {
		must(t, r.InTx(ctx, func(ctx context.Context, s app.Store) error {
			n, err := s.Transactions().WakeDependents(ctx, "provider-a", "bet-1", a.ID(), t0)
			woken = append(woken, n)
			return err
		}))
	}
	requireEqual(t, woken, []int64{1, 0})
	requireEqual(t, getTransaction(t, r, waiting.ID()).Snapshot().NextAttemptAt, t0)
	requireEqual(t, getTransaction(t, r, elsewhere.ID()).Snapshot().NextAttemptAt, t0.Add(time.Second))

	var resolved *wager.Transaction
	must(t, r.InTx(ctx, func(ctx context.Context, s app.Store) error {
		w, err := s.Wallets().GetForUpdate(ctx, a.ID())
		if err != nil {
			return err
		}
		version := w.Version()
		if resolved, err = s.Transactions().Get(ctx, waiting.ID()); err != nil {
			return err
		}
		ref, err := s.Transactions().GetByExternalID(ctx, "provider-a", "bet-1")
		if err != nil {
			return err
		}
		out, err := newRules(t).Apply(resolved, w, &wager.Reference{Transaction: ref}, t0.Add(2*time.Second))
		if err != nil {
			return err
		}
		if err := s.Transactions().UpdateState(ctx, resolved); err != nil {
			return err
		}
		return persist(ctx, s, w, version, out)
	}))

	got := getTransaction(t, r, waiting.ID()).Snapshot()
	requireEqual(t, got, resolved.Snapshot())
	if got.Status != wager.Processed || got.ReferenceTransactionID != bet.ID() || got.Result == nil || got.Result.Balance != brl(t, 10000) {
		t.Fatalf("resumed rollback = %+v; want PROCESSED against %s at 100.00", got, bet.ID())
	}
	w := getWallet(t, r, a.ID()).Snapshot()
	if w.Balance != brl(t, 10000) || w.Version != 3 {
		t.Fatalf("wallet = %s at version %d; want 100.00 at version 3", w.Balance, w.Version)
	}

	var reversed []bool
	must(t, r.InTx(ctx, func(ctx context.Context, s app.Store) error {
		reversed = nil
		for _, id := range []uuid.UUID{bet.ID(), waiting.ID(), elsewhere.ID()} {
			ok, err := s.Transactions().Reversed(ctx, id)
			if err != nil {
				return err
			}
			reversed = append(reversed, ok)
		}
		return nil
	}))
	requireEqual(t, reversed, []bool{true, false, false})
}

func TestLedgerPage(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	opened := openWallet(t, r, 10000)
	id := opened.Wallet.ID()
	want := []ledger.Entry{*opened.Entry}
	for i, minor := range []int64{1000, 2000, 3000} {
		p := payload(t, opened.Wallet, wager.Bet, fmt.Sprintf("bet-%d", i+1), minor)
		_, out := submit(t, r, p, t0.Add(time.Duration(i+1)*time.Minute))
		want = append(want, *out.Entry)
	}

	var first, second, rest, unknown []ledger.Entry
	var summary, empty app.LedgerSummary
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		var err error
		if summary, err = s.Ledger().Summarize(ctx, id, money.BRL); err != nil {
			return err
		}
		if empty, err = s.Ledger().Summarize(ctx, uuid.New(), money.BRL); err != nil {
			return err
		}
		if first, err = s.Ledger().Page(ctx, id, 0, 3); err != nil {
			return err
		}
		if second, err = s.Ledger().Page(ctx, id, 3, 3); err != nil {
			return err
		}
		if rest, err = s.Ledger().Page(ctx, id, 4, 3); err != nil {
			return err
		}
		unknown, err = s.Ledger().Page(ctx, uuid.New(), 0, 3)
		return err
	}))
	requireEqual(t, first, want[:3])
	requireEqual(t, second, want[3:])
	requireEqual(t, rest, []ledger.Entry{})
	requireEqual(t, unknown, []ledger.Entry{})
	requireEqual(t, summary, app.LedgerSummary{Entries: 4, FirstVersion: 1, LastVersion: 4, Net: brl(t, 4000)})
	requireEqual(t, empty, app.LedgerSummary{Net: brl(t, 0)})
}

func TestOutboxInsert(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	r := newRunner(t, config(db.AppURL))
	opened := openWallet(t, r, 10000)

	rows, err := db.App.Query(t.Context(), `SELECT * FROM outbox_events ORDER BY seq`)
	must(t, err)
	stored, err := pgx.CollectRows(rows, pgx.RowToStructByPos[database.OutboxEvent])
	must(t, err)
	if len(stored) != len(opened.Events) {
		t.Fatalf("%d outbox rows; want %d", len(stored), len(opened.Events))
	}
	for i, e := range opened.Events {
		h := e.EventHeader()
		got := stored[i]
		got.OccurredAt, got.NextAttemptAt = got.OccurredAt.UTC(), got.NextAttemptAt.UTC()
		var causation *string
		if h.CausationID != "" {
			causation = &h.CausationID
		}
		requireEqual(t, got, database.OutboxEvent{
			ID:            h.EventID,
			Seq:           got.Seq,
			AggregateType: h.EventType.AggregateType(),
			AggregateID:   h.AggregateID,
			PartitionKey:  opened.Wallet.ID(),
			EventType:     string(h.EventType),
			EventVersion:  1,
			CorrelationID: "corr-open",
			CausationID:   causation,
			Payload:       got.Payload,
			OccurredAt:    t0,
			NextAttemptAt: t0,
		})
		var gotJSON, wantJSON any
		must(t, json.Unmarshal(got.Payload, &gotJSON))
		encoded, err := json.Marshal(e)
		must(t, err)
		must(t, json.Unmarshal(encoded, &wantJSON))
		requireEqual(t, gotJSON, wantJSON)
	}

	err = r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		return s.Outbox().Insert(ctx, opened.Events[0])
	})
	if !errors.Is(err, app.ErrPermanent) {
		t.Fatalf("duplicate event: err = %v; want %v", err, app.ErrPermanent)
	}
}

func TestInboxRoundTrip(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	opened := openWallet(t, r, 10000)
	received := app.InboxMessage{
		Consumer:    "wager-transactions",
		MessageID:   "msg-1",
		PayloadHash: hash("msg-1"),
		ReceivedAt:  t0,
	}
	completed := received
	completed.CompletedAt, completed.Outcome, completed.TransactionID = t0.Add(time.Second), "PROCESSED", opened.Transaction.ID()
	complete := func(m app.InboxMessage) error {
		return r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
			return s.Inbox().Complete(ctx, m)
		})
	}

	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		previous, err := s.Inbox().Receive(ctx, received)
		if err != nil {
			return err
		}
		if previous != nil {
			t.Fatalf("first delivery: previous = %+v; want nil", previous)
		}
		return s.Inbox().Complete(ctx, completed)
	}))

	redelivered := received
	redelivered.PayloadHash, redelivered.ReceivedAt = hash("msg-1 changed"), t0.Add(time.Minute)
	var previous *app.InboxMessage
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		var err error
		previous, err = s.Inbox().Receive(ctx, redelivered)
		return err
	}))
	if previous == nil {
		t.Fatal("redelivery: previous = nil; want the completed message")
	}
	requireEqual(t, *previous, completed)

	if err := complete(completed); !errors.Is(err, app.ErrPermanent) {
		t.Fatalf("second completion: err = %v; want %v", err, app.ErrPermanent)
	}
	unknown := completed
	unknown.MessageID = "msg-2"
	if err := complete(unknown); !errors.Is(err, app.ErrPermanent) {
		t.Fatalf("completion without receipt: err = %v; want %v", err, app.ErrPermanent)
	}
}

func TestDueTransactionsAndMarkFailed(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	walletID, playerID := uuid.New(), uuid.New()
	pending := func(extID string, next time.Time) *wager.Transaction {
		return rehydrate(t, wager.Snapshot{
			ID:                             uuid.New(),
			WalletID:                       walletID,
			PlayerID:                       playerID,
			Kind:                           wager.Refund,
			Money:                          brl(t, 2500),
			ProviderID:                     "provider-a",
			ExternalTransactionID:          extID,
			IdempotencyKey:                 "provider-a:" + extID,
			PayloadHash:                    hash(extID),
			RoundID:                        "round-1",
			GameID:                         "game-1",
			ReferenceExternalTransactionID: "bet-" + extID,
			CorrelationID:                  "corr-" + extID,
			Status:                         wager.PendingReference,
			Attempts:                       1,
			NextAttemptAt:                  next,
			ReferenceDeadlineAt:            t0.Add(15 * time.Minute),
			CreatedAt:                      t0,
			UpdatedAt:                      t0,
		})
	}
	later := pending("refund-later", t0.Add(time.Minute))
	second := pending("refund-second", t0.Add(2*time.Second))
	first := pending("refund-first", t0.Add(time.Second))
	third := pending("refund-third", t0.Add(3*time.Second))
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		for _, tx := range []*wager.Transaction{later, second, first, third} {
			if err := s.Transactions().Insert(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	}))

	var due, limited []app.DueTransaction
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		var err error
		if due, err = s.Transactions().Due(ctx, t0.Add(10*time.Second), 10); err != nil {
			return err
		}
		limited, err = s.Transactions().Due(ctx, t0.Add(10*time.Second), 2)
		return err
	}))
	ids := func(ds []app.DueTransaction) []uuid.UUID {
		out := make([]uuid.UUID, len(ds))
		for i, d := range ds {
			if d.WalletID != walletID {
				t.Fatalf("due %s carries wallet %s; want %s", d.ID, d.WalletID, walletID)
			}
			out[i] = d.ID
		}
		return out
	}
	requireEqual(t, ids(due), []uuid.UUID{first.ID(), second.ID(), third.ID()})
	requireEqual(t, ids(limited), []uuid.UUID{first.ID(), second.ID()})

	var failed, again bool
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		locked, err := s.Transactions().GetForUpdate(ctx, first.ID())
		if err != nil {
			return err
		}
		requireEqual(t, locked.Snapshot(), first.Snapshot())
		if failed, err = s.Transactions().MarkFailed(ctx, first.ID(), t0.Add(time.Hour)); err != nil {
			return err
		}
		again, err = s.Transactions().MarkFailed(ctx, first.ID(), t0.Add(2*time.Hour))
		return err
	}))
	if !failed || again {
		t.Fatalf("MarkFailed = %v then %v; want true, then false once terminal", failed, again)
	}
	got := getTransaction(t, r, first.ID()).Snapshot()
	if got.Status != wager.Failed || got.FailureCode != wager.ProcessingFailed || !got.CompletedAt.Equal(t0.Add(time.Hour)) ||
		!got.UpdatedAt.Equal(t0.Add(time.Hour)) || got.Result != nil {
		t.Fatalf("failed transaction = %+v", got)
	}
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		var err error
		due, err = s.Transactions().Due(ctx, t0.Add(10*time.Second), 10)
		return err
	}))
	requireEqual(t, ids(due), []uuid.UUID{second.ID(), third.ID()})
}
