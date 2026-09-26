//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
)

func requireNothingPersisted(t *testing.T, db *dbtest.Database, r *postgres.TxRunner, opened wager.Opened, txID uuid.UUID) {
	t.Helper()
	must(t, r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		if _, err := s.Transactions().Get(ctx, txID); !errors.Is(err, domain.ErrTransactionNotFound) {
			t.Fatalf("transaction %s: err = %v; want %v", txID, err, domain.ErrTransactionNotFound)
		}
		w, err := s.Wallets().Get(ctx, opened.Wallet.ID())
		if err != nil {
			return err
		}
		requireEqual(t, w.Snapshot(), opened.Wallet.Snapshot())
		entries, err := s.Ledger().Page(ctx, opened.Wallet.ID(), 0, 10)
		if err != nil {
			return err
		}
		requireEqual(t, entries, []ledger.Entry{*opened.Entry})
		return nil
	}))
	var events int
	must(t, db.App.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events`).Scan(&events))
	if events != len(opened.Events) {
		t.Fatalf("%d outbox events; want the %d from the opening", events, len(opened.Events))
	}
}

func TestInTxRollsBackWhenFnFails(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	r := newRunner(t, config(db.AppURL))
	opened := openWallet(t, r, 10000)
	boom := errors.New("boom")
	var bet *wager.Transaction
	attempts := 0
	err := r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		attempts++
		w, err := s.Wallets().GetForUpdate(ctx, opened.Wallet.ID())
		if err != nil {
			return err
		}
		version := w.Version()
		bet = external(t, payload(t, opened.Wallet, wager.Bet, "bet-1", 2500), t0)
		out, err := newRules(t).Apply(bet, w, nil, t0)
		if err != nil {
			return err
		}
		if err := s.Transactions().Insert(ctx, bet); err != nil {
			return err
		}
		if err := persist(ctx, s, w, version, out); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || attempts != 1 {
		t.Fatalf("err = %v after %d attempts; want %v after 1", err, attempts, boom)
	}
	requireNothingPersisted(t, db, r, opened, bet.ID())
}

func TestInTxRollsBackWhenCommitFails(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	r := newRunner(t, config(db.AppURL))
	opened := openWallet(t, r, 10000)
	var bet *wager.Transaction
	attempts := 0
	err := r.InTx(t.Context(), func(ctx context.Context, s app.Store) error {
		attempts++
		w, err := s.Wallets().GetForUpdate(ctx, opened.Wallet.ID())
		if err != nil {
			return err
		}
		bet = external(t, payload(t, opened.Wallet, wager.Bet, "bet-1", 2500), t0)
		out, err := newRules(t).Apply(bet, w, nil, t0)
		if err != nil {
			return err
		}
		if err := s.Transactions().Insert(ctx, bet); err != nil {
			return err
		}
		return s.Ledger().Insert(ctx, *out.Entry)
	})
	var pgErr *pgconn.PgError
	if !errors.Is(err, app.ErrPermanent) || !errors.As(err, &pgErr) || pgErr.ConstraintName != "ledger_entries_wallet_coupling" || attempts != 1 {
		t.Fatalf("ledger entry without its wallet update: err = %v after %d attempts; want a permanent ledger_entries_wallet_coupling after 1", err, attempts)
	}
	requireNothingPersisted(t, db, r, opened, bet.ID())
}

func TestInTxRerunsTheLoserOfARace(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	ctx := t.Context()
	rules := newRules(t)
	p := wager.Payload{
		ProviderID:            "provider-a",
		ExternalTransactionID: "bet-1",
		PlayerID:              uuid.New(),
		WalletID:              uuid.New(),
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  wager.Bet,
		Money:                 brl(t, 2500),
	}
	key := "provider-a:bet-1"
	reject := func(ctx context.Context, s app.Store) (*wager.Transaction, error) {
		tx, err := wager.NewExternal(wager.ExternalParams{
			Payload:        p,
			ID:             uuid.New(),
			IdempotencyKey: key,
			PayloadHash:    app.PayloadHash(p),
			CorrelationID:  "corr-bet-1",
		}, t0)
		if err != nil {
			return nil, err
		}
		if _, err := rules.Apply(tx, nil, nil, t0); err != nil {
			return nil, err
		}
		return tx, s.Transactions().Insert(ctx, tx)
	}

	looked, committed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(looked) }) }
	var winner *wager.Transaction
	var winnerErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(committed)
		winnerErr = r.InTx(ctx, func(ctx context.Context, s app.Store) error {
			var err error
			winner, err = reject(ctx, s)
			<-looked
			return err
		})
	})

	attempts := 0
	var replayed *wager.Transaction
	err := r.InTx(ctx, func(ctx context.Context, s app.Store) error {
		attempts++
		existing, err := s.Transactions().GetByIdempotencyKey(ctx, p.ProviderID, key)
		switch {
		case err == nil:
			replayed = existing
			return nil
		case !errors.Is(err, domain.ErrTransactionNotFound):
			return err
		}
		release()
		<-committed
		_, err = reject(ctx, s)
		return err
	})
	release()
	wg.Wait()
	must(t, winnerErr)
	must(t, err)
	if attempts != 2 || replayed == nil || replayed.ID() != winner.ID() {
		t.Fatalf("loser ran %d attempts and found %v; want 2 attempts finding %s", attempts, replayed, winner.ID())
	}
}

func TestLockTimeoutIsTransient(t *testing.T) {
	t.Parallel()
	db := dbtest.New(t)
	cfg := config(db.AppURL)
	cfg.LockTimeout = 50 * time.Millisecond
	r := newRunner(t, cfg)
	opened := openWallet(t, r, 10000)
	ctx := t.Context()

	holder, err := db.App.Begin(ctx)
	must(t, err)
	defer func() { _ = holder.Rollback(context.Background()) }()
	_, err = holder.Exec(ctx, `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, opened.Wallet.ID())
	must(t, err)

	attempts := 0
	err = r.InTx(ctx, func(ctx context.Context, s app.Store) error {
		attempts++
		_, err := s.Wallets().GetForUpdate(ctx, opened.Wallet.ID())
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.Is(err, app.ErrTransient) || !errors.As(err, &pgErr) || pgErr.Code != "55P03" || attempts != cfg.TxAttempts {
		t.Fatalf("locked wallet: err = %v after %d attempts; want a transient 55P03 after %d", err, attempts, cfg.TxAttempts)
	}
}

func TestSessionSettings(t *testing.T) {
	t.Parallel()
	cfg := config(dbtest.New(t).AppURL)
	cfg.MaxConns = 1
	pool, err := postgres.NewPool(context.Background(), cfg)
	must(t, err)
	t.Cleanup(pool.Close)
	must(t, postgres.NewTxRunner(pool, cfg).InTx(t.Context(), func(context.Context, app.Store) error { return nil }))

	var name, lockTimeout, statementTimeout string
	must(t, pool.QueryRow(t.Context(),
		`SELECT current_setting('application_name'), current_setting('lock_timeout'), current_setting('statement_timeout')`,
	).Scan(&name, &lockTimeout, &statementTimeout))
	if name != cfg.ApplicationName || lockTimeout != "0" || statementTimeout != "0" {
		t.Fatalf("after InTx: application_name=%q lock_timeout=%q statement_timeout=%q; want %q, 0, 0",
			name, lockTimeout, statementTimeout, cfg.ApplicationName)
	}
}

func TestReadOnlySnapshot(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config(dbtest.New(t).AppURL))
	opened := openWallet(t, r, 10000)
	id := opened.Wallet.ID()
	must(t, r.InReadOnlySnapshot(t.Context(), func(ctx context.Context, s app.Store) error {
		before, err := s.Wallets().Get(ctx, id)
		if err != nil {
			return err
		}
		submit(t, r, payload(t, opened.Wallet, wager.Bet, "bet-1", 2500), t0)
		after, err := s.Wallets().Get(ctx, id)
		if err != nil {
			return err
		}
		entries, err := s.Ledger().Page(ctx, id, 0, 10)
		if err != nil {
			return err
		}
		requireEqual(t, after.Snapshot(), before.Snapshot())
		requireEqual(t, entries, []ledger.Entry{*opened.Entry})
		return nil
	}))
	requireEqual(t, getWallet(t, r, id).Snapshot().Balance, brl(t, 7500))

	err := r.InReadOnlySnapshot(t.Context(), func(ctx context.Context, s app.Store) error {
		return s.Outbox().Insert(ctx, opened.Events...)
	})
	var pgErr *pgconn.PgError
	if !errors.Is(err, app.ErrPermanent) || !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("write in a snapshot: err = %v; want a permanent 25006", err)
	}
}

func TestUnreachableDatabaseIsTransient(t *testing.T) {
	t.Parallel()
	r := newRunner(t, config("postgres://wallet_app@127.0.0.1:1/wallet?sslmode=disable"))
	calls := 0
	err := r.InTx(t.Context(), func(context.Context, app.Store) error {
		calls++
		return nil
	})
	if !errors.Is(err, app.ErrTransient) || calls != 0 {
		t.Fatalf("unreachable database: err = %v with fn called %d times; want transient before fn runs", err, calls)
	}
}
