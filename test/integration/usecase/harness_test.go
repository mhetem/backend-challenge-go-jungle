//go:build integration

package usecase_test

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/postgres"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
)

var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

type metrics struct {
	diverged atomic.Int64
}

func (m *metrics) ReconciliationDiverged() {
	m.diverged.Add(1)
}

type harness struct {
	db      *dbtest.Database
	wallets *app.WalletService
	wagers  *app.WagerService
	metrics *metrics
	logs    *bytes.Buffer
}

func setup(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	cfg := postgres.Config{
		URL:              db.AppURL,
		ApplicationName:  "usecase-test",
		MaxConns:         16,
		LockTimeout:      5 * time.Second,
		StatementTimeout: 10 * time.Second,
		TxAttempts:       5,
		TxBackoff:        5 * time.Millisecond,
	}
	pool, err := postgres.NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	runner := postgres.NewTxRunner(pool, cfg)
	rules, err := wager.NewRules(15*time.Minute, 20, func(int) time.Duration { return time.Second })
	must(t, err)
	clock := func() time.Time { return t0 }
	h := &harness{db: db, metrics: &metrics{}, logs: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(h.logs, nil))
	h.wallets = app.NewWalletService(runner, clock, app.NewID, log, h.metrics)
	h.wagers = app.NewWagerService(runner, rules, clock, app.NewID)
	return h
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.ParseSigned(amount, "BRL")
	must(t, err)
	return m
}

func openCommand(t *testing.T, playerID uuid.UUID, amount, currency string) app.OpenWallet {
	t.Helper()
	cmd, err := app.OpenWalletRequest{
		PlayerID:       playerID.String(),
		InitialBalance: app.MoneyRequest{Amount: amount, Currency: currency},
	}.Command("corr-open")
	must(t, err)
	return cmd
}

func (h *harness) open(t *testing.T, amount string) wallet.Snapshot {
	t.Helper()
	w, err := h.wallets.Open(t.Context(), openCommand(t, uuid.New(), amount, "BRL"))
	must(t, err)
	return w
}

func request(w wallet.Snapshot, kind wager.Kind, extID, amount, ref string) app.WagerRequest {
	return app.WagerRequest{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          extID,
		PlayerID:                       w.PlayerID.String(),
		WalletID:                       w.ID.String(),
		RoundID:                        "round-1",
		GameID:                         "game-1",
		Kind:                           string(kind),
		Money:                          app.MoneyRequest{Amount: amount, Currency: "BRL"},
		ReferenceExternalTransactionID: ref,
	}
}

func command(t *testing.T, r app.WagerRequest) app.SubmitWager {
	t.Helper()
	cmd, err := r.Command(r.ProviderID+":"+r.ExternalTransactionID, "corr-"+r.ExternalTransactionID)
	must(t, err)
	return cmd
}

func (h *harness) submit(t *testing.T, r app.WagerRequest) app.WagerResult {
	t.Helper()
	result, err := h.wagers.Submit(t.Context(), command(t, r))
	must(t, err)
	return result
}

func (h *harness) submitConcurrently(t *testing.T, cmds []app.SubmitWager) []app.WagerResult {
	t.Helper()
	ctx := t.Context()
	results := make([]app.WagerResult, len(cmds))
	errs := make([]error, len(cmds))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, cmd := range cmds {
		wg.Go(func() {
			<-start
			results[i], errs[i] = h.wagers.Submit(ctx, cmd)
		})
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("submission %d (%s): %v", i, cmds[i].ExternalTransactionID, err)
		}
	}
	return results
}

func (h *harness) wallet(t *testing.T, id uuid.UUID) wallet.Snapshot {
	t.Helper()
	w, err := h.wallets.Get(t.Context(), id)
	must(t, err)
	return w
}

func (h *harness) balance(t *testing.T, id uuid.UUID) string {
	t.Helper()
	return h.wallet(t, id).Balance.String()
}

func (h *harness) entries(t *testing.T, id uuid.UUID) []ledger.Snapshot {
	t.Helper()
	page, err := h.wallets.Ledger(t.Context(), id, "", app.MaxLedgerLimit)
	must(t, err)
	if page.NextCursor != "" {
		t.Fatalf("wallet %s has more than %d ledger entries", id, app.MaxLedgerLimit)
	}
	return page.Entries
}

func (h *harness) eventTypes(t *testing.T, walletID uuid.UUID) []string {
	t.Helper()
	rows, err := h.db.App.Query(t.Context(), `SELECT event_type FROM outbox_events WHERE partition_key = $1 ORDER BY seq`, walletID)
	must(t, err)
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	must(t, err)
	return types
}

func debits(entries []ledger.Snapshot) int {
	n := 0
	for _, e := range entries {
		if e.Direction == ledger.Debit {
			n++
		}
	}
	return n
}

func versions(entries []ledger.Snapshot) []int64 {
	out := make([]int64, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.WalletVersion)
	}
	return out
}
