//go:build integration

package usecase_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

func TestOpenWallet(t *testing.T) {
	t.Parallel()
	h := setup(t)
	ctx := t.Context()
	player := uuid.New()

	empty, err := h.wallets.Open(ctx, openCommand(t, player, "0.00", "BRL"))
	must(t, err)
	requireEqual(t, empty, wallet.Snapshot{
		ID:        empty.ID,
		PlayerID:  player,
		Balance:   brl(t, "0.00"),
		Version:   1,
		CreatedAt: t0,
		UpdatedAt: t0,
	})
	requireEqual(t, h.wallet(t, empty.ID), empty)
	if _, err := h.wagers.Get(ctx, wallet.OpeningTransactionID(empty.ID)); !errors.Is(err, domain.ErrTransactionNotFound) {
		t.Fatalf("zero opening transaction: err = %v; want %v", err, domain.ErrTransactionNotFound)
	}
	if entries, postings, events := h.entries(t, empty.ID), h.postings(t, empty.ID), h.eventTypes(t, empty.ID); len(entries) != 0 || len(postings) != 0 || len(events) != 0 {
		t.Fatalf("zero opening wrote %d ledger entries, postings %v and events %v; want none", len(entries), postings, events)
	}

	funded := h.open(t, "100.00")
	opening, err := h.wagers.Get(ctx, wallet.OpeningTransactionID(funded.ID))
	must(t, err)
	if opening.Kind != wager.Opening || opening.Status != wager.Processed || opening.Money != brl(t, "100.00") ||
		opening.ProviderID != "" || opening.Result == nil || opening.Result.Balance != brl(t, "100.00") || opening.Result.WalletVersion != 1 {
		t.Fatalf("opening = %+v; want an internal PROCESSED OPENING of 100.00 at version 1", opening)
	}
	requireEqual(t, h.entries(t, funded.ID), []ledger.Snapshot{{
		ID:            ledger.EntryID(opening.ID),
		WalletID:      funded.ID,
		TransactionID: opening.ID,
		Direction:     ledger.Credit,
		Amount:        brl(t, "100.00"),
		BalanceBefore: brl(t, "0.00"),
		BalanceAfter:  brl(t, "100.00"),
		WalletVersion: 1,
		CreatedAt:     t0,
	}})
	requireEqual(t, h.postings(t, funded.ID), map[string]int{"FUNDING DEBIT": 1, "PLAYER_BALANCES CREDIT": 1})
	requireEqual(t, h.eventTypes(t, funded.ID), []string{"WagerTransactionProcessed", "WalletBalanceChanged"})

	_, err = h.wallets.Open(ctx, openCommand(t, player, "50.00", "BRL"))
	var exists *app.WalletExistsError
	if !errors.As(err, &exists) || exists.WalletID != empty.ID || !errors.Is(err, domain.ErrWalletExists) {
		t.Fatalf("second BRL wallet for the player: err = %v; want %v carrying %s", err, domain.ErrWalletExists, empty.ID)
	}
	usd, err := h.wallets.Open(ctx, openCommand(t, player, "5.00", "USD"))
	must(t, err)
	var wallets int
	must(t, h.db.App.QueryRow(ctx, `SELECT count(*) FROM wallets WHERE player_id = $1`, player).Scan(&wallets))
	if usd.ID == empty.ID || wallets != 2 {
		t.Fatalf("player has %d wallets; want the BRL and the USD one", wallets)
	}
	if _, err := h.wallets.Get(ctx, uuid.New()); !errors.Is(err, domain.ErrWalletNotFound) {
		t.Fatalf("unknown wallet: err = %v; want %v", err, domain.ErrWalletNotFound)
	}
}

func TestLedgerPages(t *testing.T) {
	t.Parallel()
	h := setup(t)
	ctx := t.Context()
	w := h.open(t, "100.00")
	for _, extID := range []string{"bet-1", "bet-2", "bet-3", "bet-4"} {
		h.submit(t, request(w, wager.Bet, extID, "10.00", ""))
	}

	first, err := h.wallets.Ledger(ctx, w.ID, "", 2)
	must(t, err)
	second, err := h.wallets.Ledger(ctx, w.ID, first.NextCursor, 2)
	must(t, err)
	h.submit(t, request(w, wager.Bet, "bet-5", "10.00", ""))
	third, err := h.wallets.Ledger(ctx, w.ID, second.NextCursor, 2)
	must(t, err)
	all, err := h.wallets.Ledger(ctx, w.ID, "", 0)
	must(t, err)

	requireEqual(t, versions(first.Entries), []int64{1, 2})
	requireEqual(t, versions(second.Entries), []int64{3, 4})
	requireEqual(t, versions(third.Entries), []int64{5, 6})
	if first.NextCursor == "" || second.NextCursor == "" || third.NextCursor != "" || all.NextCursor != "" {
		t.Fatalf("cursors = %q, %q, %q, %q; want the last page and the full page without one",
			first.NextCursor, second.NextCursor, third.NextCursor, all.NextCursor)
	}
	requireEqual(t, all.Entries, slices.Concat(first.Entries, second.Entries, third.Entries))

	exact, err := h.wallets.Ledger(ctx, w.ID, "", 6)
	must(t, err)
	if len(exact.Entries) != 6 || exact.NextCursor != "" {
		t.Fatalf("page of exactly the remaining entries = %d entries, cursor %q; want 6 and none", len(exact.Entries), exact.NextCursor)
	}
	if _, err := h.wallets.Ledger(ctx, uuid.New(), "", 0); !errors.Is(err, domain.ErrWalletNotFound) {
		t.Fatalf("ledger of an unknown wallet: err = %v; want %v", err, domain.ErrWalletNotFound)
	}
}

func TestReconcile(t *testing.T) {
	t.Parallel()
	h := setup(t)
	ctx := t.Context()
	funded := h.open(t, "100.00")
	h.submit(t, request(funded, wager.Bet, "bet-1", "30.00", ""))
	h.submit(t, request(funded, wager.Win, "win-1", "10.00", ""))
	empty := h.open(t, "0.00")
	unmoved := h.open(t, "0.00")
	h.submit(t, request(empty, wager.Win, "win-2", "10.00", ""))

	reconcile := func(id uuid.UUID) app.Reconciliation {
		t.Helper()
		r, err := h.wallets.Reconcile(ctx, id)
		must(t, err)
		return r
	}
	requireEqual(t, reconcile(funded.ID), app.Reconciliation{
		WalletID:           funded.ID,
		StoredBalance:      brl(t, "80.00"),
		CalculatedBalance:  brl(t, "80.00"),
		PostedBalance:      brl(t, "80.00"),
		Difference:         brl(t, "0.00"),
		CheckedEntries:     3,
		ContinuousVersions: true,
		Consistent:         true,
	})
	if r := reconcile(empty.ID); !r.Consistent || r.CheckedEntries != 1 || r.CalculatedBalance != brl(t, "10.00") {
		t.Fatalf("wallet opened at zero then credited = %+v; want consistent at 10.00 over 1 entry", r)
	}
	if r := reconcile(unmoved.ID); !r.Consistent || r.CheckedEntries != 0 || r.CalculatedBalance != brl(t, "0.00") {
		t.Fatalf("untouched zero wallet = %+v; want consistent at 0.00 over no entries", r)
	}
	if n := h.metrics.diverged.Load(); n != 0 || h.logs.Len() != 0 {
		t.Fatalf("consistent wallets reported %d divergences and logged %q", n, h.logs.String())
	}

	admin := h.db.Admin(t)
	_, err := admin.Exec(ctx, `SET session_replication_role = replica`)
	must(t, err)
	_, err = admin.Exec(ctx, `UPDATE wallets SET balance_minor = balance_minor + 500 WHERE id = $1`, funded.ID)
	must(t, err)
	_, err = admin.Exec(ctx, `UPDATE wallets SET version = version + 1 WHERE id = $1`, empty.ID)
	must(t, err)

	requireEqual(t, reconcile(funded.ID), app.Reconciliation{
		WalletID:           funded.ID,
		StoredBalance:      brl(t, "85.00"),
		CalculatedBalance:  brl(t, "80.00"),
		PostedBalance:      brl(t, "80.00"),
		Difference:         brl(t, "5.00"),
		CheckedEntries:     3,
		ContinuousVersions: true,
		Consistent:         false,
	})
	if r := reconcile(empty.ID); r.Consistent || r.ContinuousVersions || r.Difference != brl(t, "0.00") {
		t.Fatalf("wallet whose version skipped = %+v; want inconsistent versions with no difference", r)
	}
	if n := h.metrics.diverged.Load(); n != 2 {
		t.Fatalf("%d divergences counted; want 2", n)
	}
	if logs := h.logs.String(); strings.Count(logs, `"level":"WARN"`) != 2 || !strings.Contains(logs, funded.ID.String()) {
		t.Fatalf("logs = %q; want two WARN lines naming the wallets", logs)
	}
	if got, entries := h.balance(t, funded.ID), len(h.entries(t, funded.ID)); got != "85.00" || entries != 3 {
		t.Fatalf("after reconciling: balance %s over %d entries; want it untouched at 85.00 over 3", got, entries)
	}
}

func TestTrialBalance(t *testing.T) {
	t.Parallel()
	h := setup(t)
	ctx := t.Context()
	a, b := h.open(t, "100.00"), h.open(t, "0.00")
	_, err := h.wallets.Open(ctx, openCommand(t, uuid.New(), "5.00", "USD"))
	must(t, err)
	for _, r := range []app.WagerRequest{
		request(a, wager.Bet, "bet-1", "30.00", ""),
		request(a, wager.Win, "win-1", "10.00", ""),
		request(a, wager.Refund, "refund-1", "30.00", "bet-1"),
		request(a, wager.Rollback, "rollback-1", "10.00", "win-1"),
		request(a, wager.Loss, "loss-1", "0.00", ""),
		request(b, wager.Win, "win-2", "10.00", ""),
		request(b, wager.Bet, "bet-2", "500.00", ""),
	} {
		h.submit(t, r)
	}
	usd := func(amount string) money.Money {
		m, err := money.ParseSigned(amount, "USD")
		must(t, err)
		return m
	}
	want := []app.TrialBalance{
		{
			Ledger: ledger.TrialBalance{
				Currency: money.BRL,
				Accounts: []ledger.AccountTotals{
					{Account: ledger.Funding, Postings: 1, Debits: brl(t, "100.00"), Credits: brl(t, "0.00"), Balance: brl(t, "100.00")},
					{Account: ledger.PlayerBalances, Postings: 6, Debits: brl(t, "40.00"), Credits: brl(t, "150.00"), Balance: brl(t, "110.00")},
					{Account: ledger.GamingRevenue, Postings: 5, Debits: brl(t, "50.00"), Credits: brl(t, "40.00"), Balance: brl(t, "-10.00")},
				},
				Debits:  brl(t, "190.00"),
				Credits: brl(t, "190.00"),
			},
			Wallets:        2,
			WalletBalances: brl(t, "110.00"),
			Consistent:     true,
		},
		{
			Ledger: ledger.TrialBalance{
				Currency: money.USD,
				Accounts: []ledger.AccountTotals{
					{Account: ledger.Funding, Postings: 1, Debits: usd("5.00"), Credits: usd("0.00"), Balance: usd("5.00")},
					{Account: ledger.PlayerBalances, Postings: 1, Debits: usd("0.00"), Credits: usd("5.00"), Balance: usd("5.00")},
					{Account: ledger.GamingRevenue, Debits: usd("0.00"), Credits: usd("0.00"), Balance: usd("0.00")},
				},
				Debits:  usd("5.00"),
				Credits: usd("5.00"),
			},
			Wallets:        1,
			WalletBalances: usd("5.00"),
			Consistent:     true,
		},
	}
	got, err := h.wallets.TrialBalance(ctx)
	must(t, err)
	requireEqual(t, got, want)
	if n := h.metrics.diverged.Load(); n != 0 || h.logs.Len() != 0 {
		t.Fatalf("consistent books reported %d divergences and logged %q", n, h.logs.String())
	}

	admin := h.db.Admin(t)
	_, err = admin.Exec(ctx, `SET session_replication_role = replica`)
	must(t, err)
	_, err = admin.Exec(ctx, `UPDATE wallets SET balance_minor = balance_minor + 500 WHERE id = $1`, b.ID)
	must(t, err)

	got, err = h.wallets.TrialBalance(ctx)
	must(t, err)
	brlBooks := got[0]
	if brlBooks.Consistent || !brlBooks.Ledger.Balanced() || brlBooks.WalletBalances != brl(t, "115.00") || !got[1].Consistent {
		t.Fatalf("books after a stored balance drifted = %+v; want balanced BRL books that no longer match 115.00 in wallets", got)
	}
	if n := h.metrics.diverged.Load(); n != 1 || !strings.Contains(h.logs.String(), `"currency":"BRL"`) {
		t.Fatalf("%d divergences counted, logs %q; want the BRL books reported once", n, h.logs.String())
	}
}
