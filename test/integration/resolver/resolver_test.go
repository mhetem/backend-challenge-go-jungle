//go:build integration

package resolver_test

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
	"github.com/mhetem/backend-challenge-go-jungle/internal/workers/resolver"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
)

var (
	t0      = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	discard = slog.New(slog.DiscardHandler)
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type instance struct {
	wallets  *app.WalletService
	wagers   *app.WagerService
	resolver *resolver.Resolver
	registry *prometheus.Registry
}

type harness struct {
	t     *testing.T
	db    *dbtest.Database
	clock *clock
}

func setup(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t, db: dbtest.New(t), clock: &clock{now: t0}}
}

func (h *harness) instance() *instance {
	h.t.Helper()
	cfg := postgres.Config{
		URL:              h.db.AppURL,
		ApplicationName:  "resolver-test",
		MaxConns:         8,
		LockTimeout:      5 * time.Second,
		StatementTimeout: 10 * time.Second,
		TxAttempts:       3,
		TxBackoff:        5 * time.Millisecond,
	}
	pool, err := postgres.NewPool(context.Background(), cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(pool.Close)
	runner := postgres.NewTxRunner(pool, cfg)
	rules, err := wager.NewRules(10*time.Second, 20, func(int) time.Duration { return time.Second })
	if err != nil {
		h.t.Fatal(err)
	}
	in := &instance{
		wallets:  app.NewWalletService(runner, h.clock.Now, app.NewID, discard, noMetrics{}),
		wagers:   app.NewWagerService(runner, rules, h.clock.Now, app.NewID),
		registry: prometheus.NewRegistry(),
	}
	in.resolver, err = resolver.New(config.ResolverConfig{PollInterval: 10 * time.Millisecond, BatchSize: 100, MaxFailures: 3},
		in.wagers, in.registry, discard)
	if err != nil {
		h.t.Fatal(err)
	}
	return in
}

type noMetrics struct{}

func (noMetrics) ReconciliationDiverged() {}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (in *instance) open(t *testing.T, amount string) wallet.Snapshot {
	t.Helper()
	cmd, err := app.OpenWalletRequest{
		PlayerID:       uuid.NewString(),
		InitialBalance: app.MoneyRequest{Amount: amount, Currency: "BRL"},
	}.Command("corr-open")
	must(t, err)
	w, err := in.wallets.Open(t.Context(), cmd)
	must(t, err)
	return w
}

func (in *instance) submit(t *testing.T, w wallet.Snapshot, kind, extID, amount, ref string) wager.Snapshot {
	t.Helper()
	cmd, err := app.WagerRequest{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          extID,
		PlayerID:                       w.PlayerID.String(),
		WalletID:                       w.ID.String(),
		RoundID:                        "round-1",
		GameID:                         "game-1",
		Kind:                           kind,
		Money:                          app.MoneyRequest{Amount: amount, Currency: "BRL"},
		ReferenceExternalTransactionID: ref,
	}.Command("provider-a:"+extID, "corr-"+extID)
	must(t, err)
	result, err := in.wagers.Submit(t.Context(), cmd)
	must(t, err)
	return result.Transaction
}

func (in *instance) get(t *testing.T, id uuid.UUID) wager.Snapshot {
	t.Helper()
	tx, err := in.wagers.Get(t.Context(), id)
	must(t, err)
	return tx
}

func (in *instance) balance(t *testing.T, id uuid.UUID) string {
	t.Helper()
	w, err := in.wallets.Get(t.Context(), id)
	must(t, err)
	return w.Balance.String()
}

func (in *instance) outcomes(t *testing.T, outcome string) int {
	t.Helper()
	families, err := in.registry.Gather()
	must(t, err)
	for _, f := range families {
		for _, m := range f.GetMetric() {
			if f.GetName() == "wallet_resolver_outcomes_total" && m.GetLabel()[0].GetValue() == outcome {
				return int(m.GetCounter().GetValue())
			}
		}
	}
	return 0
}

func (h *harness) events(transactionID uuid.UUID) []string {
	h.t.Helper()
	rows, err := h.db.App.Query(h.t.Context(), `SELECT event_type FROM outbox_events
		WHERE aggregate_id = $1 OR causation_id = $2 ORDER BY seq`, transactionID, transactionID.String())
	must(h.t, err)
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	must(h.t, err)
	return types
}

func requireStatus(t *testing.T, s wager.Snapshot, status wager.Status, code wager.FailureCode) {
	t.Helper()
	if s.Status != status || s.FailureCode != code {
		t.Fatalf("%s %s = %s %s; want %s %s", s.Kind, s.ExternalTransactionID, s.Status, s.FailureCode, status, code)
	}
}

func TestRollbackBeforeItsBetSettlesToNetZero(t *testing.T) {
	t.Parallel()
	h := setup(t)
	in := h.instance()
	w := in.open(t, "100.00")

	rollback := in.submit(t, w, "ROLLBACK", "rollback-1", "25.00", "bet-1")
	requireStatus(t, rollback, wager.PendingReference, "")
	bet := in.submit(t, w, "BET", "bet-1", "25.00", "")
	requireStatus(t, bet, wager.Processed, "")
	if got := in.balance(t, w.ID); got != "75.00" {
		t.Fatalf("balance after the bet = %s; want 75.00", got)
	}

	in.resolver.Tick(t.Context())
	settled := in.get(t, rollback.ID)
	requireStatus(t, settled, wager.Processed, "")
	if settled.ReferenceTransactionID != bet.ID || settled.Result == nil || settled.Result.Balance.String() != "100.00" || settled.Result.WalletVersion != 3 {
		t.Fatalf("settled rollback = %+v; want linked to %s at 100.00, version 3", settled, bet.ID)
	}
	if got := in.balance(t, w.ID); got != "100.00" {
		t.Fatalf("balance = %s; want 100.00, net zero", got)
	}
	want := []string{"WagerTransactionPendingReference", "WagerTransactionProcessed", "WalletBalanceChanged"}
	if got := h.events(rollback.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("rollback events = %v; want %v", got, want)
	}
	r, err := in.wallets.Reconcile(t.Context(), w.ID)
	must(t, err)
	if !r.Consistent || r.CheckedEntries != 3 {
		t.Fatalf("reconciliation = %+v; want consistent over 3 entries", r)
	}
	if n := in.outcomes(t, "processed"); n != 1 {
		t.Fatalf("processed outcomes = %d; want 1", n)
	}
	in.resolver.Tick(t.Context())
	if n := in.outcomes(t, "processed"); n != 1 {
		t.Fatalf("processed outcomes after another tick = %d; the settled rollback is no longer due", n)
	}
}

func TestReferencesThatNeverSettleAreRejected(t *testing.T) {
	t.Parallel()
	h := setup(t)
	in := h.instance()
	w := in.open(t, "100.00")

	missing := in.submit(t, w, "REFUND", "refund-1", "25.00", "bet-never")
	waitingOnPending := in.submit(t, w, "ROLLBACK", "rollback-2", "25.00", "refund-2")
	waitingOnRejected := in.submit(t, w, "ROLLBACK", "rollback-3", "500.00", "bet-3")

	in.resolver.Tick(t.Context())
	if n := in.outcomes(t, "rescheduled"); n != 0 {
		t.Fatalf("%d attempts before anything was due", n)
	}

	h.clock.Set(t0.Add(2 * time.Second))
	in.resolver.Tick(t.Context())
	if got := in.get(t, missing.ID); got.Status != wager.PendingReference || got.Attempts != 2 || !got.NextAttemptAt.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("refund after one retry = %s, attempts %d, next %s", got.Status, got.Attempts, got.NextAttemptAt)
	}

	h.clock.Set(t0.Add(5 * time.Second))
	refund2 := in.submit(t, w, "REFUND", "refund-2", "25.00", "bet-never-2")
	requireStatus(t, refund2, wager.PendingReference, "")
	bet3 := in.submit(t, w, "BET", "bet-3", "500.00", "")
	requireStatus(t, bet3, wager.Rejected, wager.InsufficientFunds)
	in.resolver.Tick(t.Context())
	requireStatus(t, in.get(t, waitingOnRejected.ID), wager.Rejected, wager.ReferenceNotProcessed)

	h.clock.Set(t0.Add(11 * time.Second))
	in.resolver.Tick(t.Context())
	requireStatus(t, in.get(t, missing.ID), wager.Rejected, wager.ReferenceNotFound)
	requireStatus(t, in.get(t, waitingOnPending.ID), wager.Rejected, wager.ReferenceNotSettled)
	if got := in.get(t, refund2.ID); got.Status != wager.PendingReference {
		t.Fatalf("refund-2 = %s before its own deadline; want still PENDING_REFERENCE", got.Status)
	}
	for _, id := range []uuid.UUID{missing.ID, waitingOnPending.ID, waitingOnRejected.ID} {
		want := []string{"WagerTransactionPendingReference", "WagerTransactionRejected"}
		if got := h.events(id); !reflect.DeepEqual(got, want) {
			t.Fatalf("events of %s = %v; want %v", id, got, want)
		}
	}
	if got := in.balance(t, w.ID); got != "100.00" {
		t.Fatalf("balance = %s; nothing should have moved", got)
	}
}

func TestCompetingResolversSettleEachTransactionOnce(t *testing.T) {
	t.Parallel()
	h := setup(t)
	a, b := h.instance(), h.instance()
	const wallets = 20
	var rollbacks []wager.Snapshot
	var ws []wallet.Snapshot
	for i := range wallets {
		w := a.open(t, "100.00")
		rollbacks = append(rollbacks, a.submit(t, w, "ROLLBACK", fmt.Sprintf("rollback-%d", i), "25.00", fmt.Sprintf("bet-%d", i)))
		a.submit(t, w, "BET", fmt.Sprintf("bet-%d", i), "25.00", "")
		ws = append(ws, w)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, in := range []*instance{a, b} {
		wg.Go(func() {
			<-start
			in.resolver.Tick(context.Background())
		})
	}
	close(start)
	wg.Wait()

	if processed := a.outcomes(t, "processed") + b.outcomes(t, "processed"); processed != wallets {
		t.Fatalf("%d rollbacks settled across both resolvers; want each of the %d exactly once", processed, wallets)
	}
	if errs := a.outcomes(t, "error") + b.outcomes(t, "error"); errs != 0 {
		t.Fatalf("%d errors while competing", errs)
	}
	for i, rb := range rollbacks {
		requireStatus(t, a.get(t, rb.ID), wager.Processed, "")
		if got := a.balance(t, ws[i].ID); got != "100.00" {
			t.Fatalf("wallet %d = %s; want 100.00", i, got)
		}
		page, err := a.wallets.Ledger(t.Context(), ws[i].ID, "", 0)
		must(t, err)
		if len(page.Entries) != 3 {
			t.Fatalf("wallet %d has %d ledger entries; want opening, bet and one rollback credit", i, len(page.Entries))
		}
	}
}

func TestStoppedResolverLeavesNothingHalfApplied(t *testing.T) {
	t.Parallel()
	h := setup(t)
	in := h.instance()
	w := in.open(t, "100.00")
	rollback := in.submit(t, w, "ROLLBACK", "rollback-1", "25.00", "bet-1")
	in.submit(t, w, "BET", "bet-1", "25.00", "")
	before := in.get(t, rollback.ID)
	eventsBefore := h.events(rollback.ID)

	ctx := t.Context()
	holder, err := h.db.App.Begin(ctx)
	must(t, err)
	defer func() { _ = holder.Rollback(context.Background()) }()
	_, err = holder.Exec(ctx, `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, w.ID)
	must(t, err)

	worker := lifecycle.NewWorker("resolver", 1, in.resolver.Run, discard)
	must(t, worker.Start(ctx))
	deadline := time.Now().Add(5 * time.Second)
	for waiting := 0; waiting == 0; {
		must(t, h.db.App.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting))
		if time.Now().After(deadline) {
			t.Fatal("the resolver never blocked on the wallet lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	must(t, worker.Stop(ctx))
	must(t, holder.Rollback(ctx))

	if after := in.get(t, rollback.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("the interrupted attempt changed the rollback:\nbefore %+v\nafter  %+v", before, after)
	}
	if got := in.balance(t, w.ID); got != "75.00" {
		t.Fatalf("balance = %s; want 75.00, untouched", got)
	}
	if got := h.events(rollback.ID); !reflect.DeepEqual(got, eventsBefore) {
		t.Fatalf("events = %v; want %v", got, eventsBefore)
	}

	in.resolver.Tick(ctx)
	requireStatus(t, in.get(t, rollback.ID), wager.Processed, "")
	if got := in.balance(t, w.ID); got != "100.00" {
		t.Fatalf("balance after the next run = %s; want 100.00", got)
	}
}
