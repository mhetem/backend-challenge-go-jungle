package wager_test

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

var (
	processedTypes = []events.Type{events.TypeWagerTransactionProcessed, events.TypeWalletBalanceChanged}
	lossTypes      = []events.Type{events.TypeWagerTransactionProcessed}
	rejectedTypes  = []events.Type{events.TypeWagerTransactionRejected}
	pendingTypes   = []events.Type{events.TypeWagerTransactionPendingReference}
)

func referenced(t *testing.T, extID string, kind wager.Kind, minor int64, status wager.Status) *wager.Transaction {
	t.Helper()
	if kind == wager.Opening {
		tx, err := wager.NewOpening(walletID, playerID, brl(t, minor), "corr-open", t0)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	ref := ""
	if kind.Reversal() || kind == wager.Win {
		ref = "origin-0"
	}
	return stored(t, params(t, extID, kind, minor, ref), status)
}

func TestApplyBet(t *testing.T) {
	tx := pending(t, params(t, "bet-1", wager.Bet, 2500, ""))
	w := holder(t, 100000)
	out, err := rules(t).Apply(tx, w, nil, t1)
	if err != nil {
		t.Fatal(err)
	}
	wantEntry := ledger.Snapshot{
		ID:            ledger.EntryID(tx.ID()),
		WalletID:      walletID,
		TransactionID: tx.ID(),
		Direction:     ledger.Debit,
		Amount:        brl(t, 2500),
		BalanceBefore: brl(t, 100000),
		BalanceAfter:  brl(t, 97500),
		WalletVersion: 6,
		CreatedAt:     t1,
	}
	if out.Entry == nil || out.Entry.Snapshot() != wantEntry {
		t.Fatalf("entry = %+v; want %+v", out.Entry, wantEntry)
	}
	wantJournal := []ledger.Posting{
		{WalletID: walletID, TransactionID: tx.ID(), Account: ledger.PlayerBalances, Direction: ledger.Debit, Amount: brl(t, 2500), CreatedAt: t1},
		{WalletID: walletID, TransactionID: tx.ID(), Account: ledger.GamingRevenue, Direction: ledger.Credit, Amount: brl(t, 2500), CreatedAt: t1},
	}
	if got := out.Journal.Postings(); !reflect.DeepEqual(got, wantJournal) {
		t.Fatalf("journal = %+v; want %+v", got, wantJournal)
	}
	s := tx.Snapshot()
	if s.Status != wager.Processed || *s.Result != (wager.Result{Balance: brl(t, 97500), WalletVersion: 6}) || s.CompletedAt != t1 {
		t.Fatalf("transaction = %+v", s)
	}
	if w.Balance() != brl(t, 97500) || w.Version() != 6 {
		t.Fatalf("wallet = %+v", w.Snapshot())
	}
	data := events.TransactionData{
		TransactionID:         tx.ID(),
		Origin:                "EXTERNAL",
		ProviderID:            "provider-a",
		ExternalTransactionID: "bet-1",
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 brl(t, 2500),
	}
	want := []events.Event{
		events.NewWagerTransactionProcessed(events.Meta{
			EventID:       eventID(tx.ID(), events.TypeWagerTransactionProcessed),
			CorrelationID: "corr-bet-1",
			OccurredAt:    t1,
		}, events.WagerTransactionProcessedData{TransactionData: data, Balance: brl(t, 97500), WalletVersion: 6}),
		events.NewWalletBalanceChanged(events.Meta{
			EventID:       eventID(tx.ID(), events.TypeWalletBalanceChanged),
			CorrelationID: "corr-bet-1",
			CausationID:   tx.ID().String(),
			OccurredAt:    t1,
		}, events.WalletBalanceChangedData{
			WalletID:      walletID,
			TransactionID: tx.ID(),
			Direction:     "DEBIT",
			Money:         brl(t, 2500),
			BalanceBefore: brl(t, 100000),
			BalanceAfter:  brl(t, 97500),
			WalletVersion: 6,
		}),
	}
	if !reflect.DeepEqual(out.Events, want) {
		t.Fatalf("events = %+v; want %+v", out.Events, want)
	}
}

func TestApplyKinds(t *testing.T) {
	type ref struct {
		kind  wager.Kind
		minor int64
	}
	tests := []struct {
		name    string
		kind    wager.Kind
		minor   int64
		ref     *ref
		balance int64
		status  wager.Status
		code    wager.FailureCode
		dir     ledger.Direction
		after   int64
	}{
		{"bet", wager.Bet, 2500, nil, 10000, wager.Processed, "", ledger.Debit, 7500},
		{"bet of the whole balance", wager.Bet, 10000, nil, 10000, wager.Processed, "", ledger.Debit, 0},
		{"bet over the balance", wager.Bet, 10001, nil, 10000, wager.Rejected, wager.InsufficientFunds, "", 10000},
		{"win", wager.Win, 5000, nil, 10000, wager.Processed, "", ledger.Credit, 15000},
		{"win of a bet", wager.Win, 5000, &ref{wager.Bet, 2500}, 10000, wager.Processed, "", ledger.Credit, 15000},
		{"loss", wager.Loss, 0, nil, 10000, wager.Processed, "", "", 10000},
		{"loss on an empty wallet", wager.Loss, 0, nil, 0, wager.Processed, "", "", 0},
		{"refund", wager.Refund, 2500, &ref{wager.Bet, 2500}, 10000, wager.Processed, "", ledger.Credit, 12500},
		{"rollback of a bet", wager.Rollback, 2500, &ref{wager.Bet, 2500}, 10000, wager.Processed, "", ledger.Credit, 12500},
		{"rollback of a win", wager.Rollback, 5000, &ref{wager.Win, 5000}, 10000, wager.Processed, "", ledger.Debit, 5000},
		{"rollback of a refund", wager.Rollback, 2500, &ref{wager.Refund, 2500}, 10000, wager.Processed, "", ledger.Debit, 7500},
		{"rollback of a win over the balance", wager.Rollback, 15000, &ref{wager.Win, 15000}, 10000, wager.Rejected, wager.ReversalInsufficientFunds, "", 10000},
		{"rollback of a refund over the balance", wager.Rollback, 15000, &ref{wager.Refund, 15000}, 10000, wager.Rejected, wager.ReversalInsufficientFunds, "", 10000},
		{"win overflow", wager.Win, 1, nil, math.MaxInt64, wager.Rejected, wager.BalanceOverflow, "", math.MaxInt64},
		{"refund overflow", wager.Refund, 2500, &ref{wager.Bet, 2500}, math.MaxInt64, wager.Rejected, wager.BalanceOverflow, "", math.MaxInt64},
		{"rollback of a bet overflow", wager.Rollback, 2500, &ref{wager.Bet, 2500}, math.MaxInt64, wager.Rejected, wager.BalanceOverflow, "", math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reference *wager.Reference
			refExtID := ""
			if tt.ref != nil {
				refExtID = "ref-1"
				reference = &wager.Reference{Transaction: referenced(t, refExtID, tt.ref.kind, tt.ref.minor, wager.Processed)}
			}
			tx := pending(t, params(t, "tx-1", tt.kind, tt.minor, refExtID))
			w := holder(t, tt.balance)
			out, err := rules(t).Apply(tx, w, reference, t1)
			if err != nil {
				t.Fatal(err)
			}
			if tx.Status() != tt.status || tx.FailureCode() != tt.code {
				t.Fatalf("transaction = %s %s; want %s %s", tx.Status(), tx.FailureCode(), tt.status, tt.code)
			}
			version, wantTypes := int64(5), rejectedTypes
			journal := out.Journal.Postings()
			switch {
			case tt.dir != "":
				version, wantTypes = 6, processedTypes
				if out.Entry == nil || out.Entry.Snapshot().Direction != tt.dir || out.Entry.Snapshot().BalanceAfter != brl(t, tt.after) {
					t.Fatalf("entry = %+v; want %s leaving %s", out.Entry, tt.dir, brl(t, tt.after))
				}
				if len(journal) != 2 || journal[0].Account != ledger.PlayerBalances || journal[0].Direction != tt.dir ||
					journal[1].Account != ledger.GamingRevenue || journal[1].Direction != tt.dir.Opposite() || journal[1].Amount != brl(t, tt.minor) {
					t.Fatalf("journal = %+v; want the player %s mirrored on %s", journal, tt.dir, ledger.GamingRevenue)
				}
			case out.Entry != nil, journal != nil:
				t.Fatalf("unexpected entry %+v or journal %+v", out.Entry, journal)
			case tt.status == wager.Processed:
				wantTypes = lossTypes
			}
			if w.Balance() != brl(t, tt.after) || w.Version() != version {
				t.Fatalf("wallet = %s v%d; want %s v%d", w.Balance(), w.Version(), brl(t, tt.after), version)
			}
			if r := tx.Snapshot().Result; r == nil || *r != (wager.Result{Balance: brl(t, tt.after), WalletVersion: version}) {
				t.Fatalf("result = %+v; want %s v%d", r, brl(t, tt.after), version)
			}
			if got := eventTypes(out.Events); !slices.Equal(got, wantTypes) {
				t.Fatalf("events = %v; want %v", got, wantTypes)
			}
			if reference != nil && tx.Snapshot().ReferenceTransactionID != reference.Transaction.ID() {
				t.Fatalf("reference not linked: %s", tx.Snapshot().ReferenceTransactionID)
			}
		})
	}
}

func TestApplyWalletChecks(t *testing.T) {
	strangers, err := wallet.Rehydrate(wallet.Snapshot{
		ID: walletID, PlayerID: otherPlayerID, Balance: brl(t, 10000), Version: 5, CreatedAt: t0, UpdatedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	usd := params(t, "bet-1", wager.Bet, 2500, "")
	usd.Money = amount(t, 2500, money.USD)
	tests := []struct {
		name   string
		params wager.ExternalParams
		wallet *wallet.Wallet
		code   wager.FailureCode
	}{
		{"missing wallet", params(t, "bet-1", wager.Bet, 2500, ""), nil, wager.WalletNotFound},
		{"missing wallet before a missing reference", params(t, "refund-1", wager.Refund, 2500, "bet-1"), nil, wager.WalletNotFound},
		{"another player's wallet", params(t, "bet-1", wager.Bet, 2500, ""), strangers, wager.WalletPlayerMismatch},
		{"another currency", usd, holder(t, 10000), wager.CurrencyMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := pending(t, tt.params)
			var before wallet.Snapshot
			if tt.wallet != nil {
				before = tt.wallet.Snapshot()
			}
			out, err := rules(t).Apply(tx, tt.wallet, nil, t1)
			if err != nil {
				t.Fatal(err)
			}
			s := tx.Snapshot()
			if s.Status != wager.Rejected || s.FailureCode != tt.code || s.Result != nil || out.Entry != nil {
				t.Fatalf("transaction = %+v; want REJECTED %s without a result", s, tt.code)
			}
			if tt.wallet != nil && tt.wallet.Snapshot() != before {
				t.Fatalf("wallet changed to %+v", tt.wallet.Snapshot())
			}
			if len(out.Events) != 1 {
				t.Fatalf("events = %+v", out.Events)
			}
			ev, ok := out.Events[0].(events.WagerTransactionRejected)
			if !ok || ev.Data.FailureCode != string(tt.code) || ev.Data.Balance != (money.Money{}) || ev.Data.WalletVersion != 0 {
				t.Fatalf("rejected event = %+v; want %s without a balance", out.Events[0], tt.code)
			}
		})
	}
}

func TestApplyReferenceMatrix(t *testing.T) {
	verdicts := []struct {
		kind wager.Kind
		ref  wager.Kind
		code wager.FailureCode
		dir  ledger.Direction
	}{
		{wager.Win, wager.Opening, wager.ReferenceMismatch, ""},
		{wager.Win, wager.Bet, "", ledger.Credit},
		{wager.Win, wager.Win, wager.ReferenceMismatch, ""},
		{wager.Win, wager.Loss, wager.ReferenceMismatch, ""},
		{wager.Win, wager.Refund, wager.ReferenceMismatch, ""},
		{wager.Win, wager.Rollback, wager.ReferenceMismatch, ""},
		{wager.Refund, wager.Opening, wager.ReferenceKindNotReversible, ""},
		{wager.Refund, wager.Bet, "", ledger.Credit},
		{wager.Refund, wager.Win, wager.ReferenceKindNotReversible, ""},
		{wager.Refund, wager.Loss, wager.ReferenceKindNotReversible, ""},
		{wager.Refund, wager.Refund, wager.ReferenceKindNotReversible, ""},
		{wager.Refund, wager.Rollback, wager.ReferenceKindNotReversible, ""},
		{wager.Rollback, wager.Opening, wager.ReferenceKindNotReversible, ""},
		{wager.Rollback, wager.Bet, "", ledger.Credit},
		{wager.Rollback, wager.Win, "", ledger.Debit},
		{wager.Rollback, wager.Loss, wager.ReferenceKindNotReversible, ""},
		{wager.Rollback, wager.Refund, "", ledger.Debit},
		{wager.Rollback, wager.Rollback, wager.ReferenceKindNotReversible, ""},
	}
	statuses := []struct {
		ref    wager.Status
		status wager.Status
		code   wager.FailureCode
	}{
		{wager.PendingReference, wager.PendingReference, ""},
		{wager.Processed, wager.Processed, ""},
		{wager.Rejected, wager.Rejected, wager.ReferenceNotProcessed},
		{wager.Failed, wager.Rejected, wager.ReferenceNotProcessed},
	}
	for _, v := range verdicts {
		for _, st := range statuses {
			if v.ref == wager.Opening && st.ref != wager.Processed {
				continue
			}
			if st.ref == wager.PendingReference && (v.ref == wager.Bet || v.ref == wager.Loss || v.ref == wager.Opening) {
				continue
			}
			t.Run(string(v.kind)+"/"+string(v.ref)+"/"+string(st.ref), func(t *testing.T) {
				refMinor := int64(2500)
				if v.ref == wager.Loss {
					refMinor = 0
				}
				ref := referenced(t, "ref-1", v.ref, refMinor, st.ref)
				tx := pending(t, params(t, "tx-1", v.kind, 2500, "ref-1"))
				w := holder(t, 100000)
				out, err := rules(t).Apply(tx, w, &wager.Reference{Transaction: ref}, t1)
				if err != nil {
					t.Fatal(err)
				}
				status, code := wager.Rejected, v.code
				if code == "" {
					status, code = st.status, st.code
				}
				if tx.Status() != status || tx.FailureCode() != code {
					t.Fatalf("transaction = %s %s; want %s %s", tx.Status(), tx.FailureCode(), status, code)
				}
				if tx.Snapshot().ReferenceTransactionID != ref.ID() {
					t.Fatalf("reference not linked")
				}
				switch status {
				case wager.Processed:
					if out.Entry == nil || out.Entry.Snapshot().Direction != v.dir {
						t.Fatalf("entry = %+v; want %s", out.Entry, v.dir)
					}
				case wager.PendingReference:
					if out.Entry != nil || !slices.Equal(eventTypes(out.Events), pendingTypes) {
						t.Fatalf("outcome = %+v; want only a pending-reference event", out)
					}
				default:
					if out.Entry != nil || !slices.Equal(eventTypes(out.Events), rejectedTypes) {
						t.Fatalf("outcome = %+v; want only a rejected event", out)
					}
				}
			})
		}
	}
}

func TestApplyMissingReferenceAwaits(t *testing.T) {
	for _, kind := range []wager.Kind{wager.Win, wager.Refund, wager.Rollback} {
		t.Run(string(kind), func(t *testing.T) {
			tx := pending(t, params(t, "tx-1", kind, 2500, "ref-1"))
			out, err := rules(t).Apply(tx, holder(t, 10000), nil, t1)
			if err != nil {
				t.Fatal(err)
			}
			s := tx.Snapshot()
			if s.Status != wager.PendingReference || s.ReferenceTransactionID != uuid.Nil || out.Entry != nil {
				t.Fatalf("transaction = %+v", s)
			}
			if !slices.Equal(eventTypes(out.Events), pendingTypes) {
				t.Fatalf("events = %v", eventTypes(out.Events))
			}
		})
	}
}

func TestApplyReferenceMismatches(t *testing.T) {
	tests := []struct {
		name      string
		kind      wager.Kind
		minor     int64
		refKind   wager.Kind
		refStatus wager.Status
		mutate    func(*wager.ExternalParams)
		status    wager.Status
		code      wager.FailureCode
	}{
		{"provider", wager.Refund, 2500, wager.Bet, wager.Processed, func(p *wager.ExternalParams) { p.ProviderID = "provider-b" },
			wager.Rejected, wager.ReferenceMismatch},
		{"player", wager.Refund, 2500, wager.Bet, wager.Processed, func(p *wager.ExternalParams) { p.PlayerID = otherPlayerID },
			wager.Rejected, wager.ReferenceMismatch},
		{"wallet", wager.Rollback, 2500, wager.Bet, wager.Processed, func(p *wager.ExternalParams) { p.WalletID = otherWalletID },
			wager.Rejected, wager.ReferenceMismatch},
		{"round", wager.Rollback, 2500, wager.Bet, wager.Processed, func(p *wager.ExternalParams) { p.RoundID = "round-988" },
			wager.Rejected, wager.ReferenceMismatch},
		{"currency", wager.Refund, 2500, wager.Bet, wager.Processed, func(p *wager.ExternalParams) { p.Money = amount(t, 2500, money.USD) },
			wager.Rejected, wager.ReferenceMismatch},
		{"refund amount", wager.Refund, 2500, wager.Bet, wager.Processed, func(p *wager.ExternalParams) { p.Money = brl(t, 3000) },
			wager.Rejected, wager.ReferenceAmountMismatch},
		{"rollback amount", wager.Rollback, 2500, wager.Bet, wager.Processed, func(p *wager.ExternalParams) { p.Money = brl(t, 2000) },
			wager.Rejected, wager.ReferenceAmountMismatch},
		{"win of a smaller bet", wager.Win, 5000, wager.Bet, wager.Processed, func(p *wager.ExternalParams) {},
			wager.Processed, ""},
		{"win in another round", wager.Win, 5000, wager.Bet, wager.Processed, func(p *wager.ExternalParams) { p.RoundID = "round-988" },
			wager.Rejected, wager.ReferenceMismatch},
		{"mismatch before waiting", wager.Rollback, 2500, wager.Win, wager.PendingReference, func(p *wager.ExternalParams) { p.RoundID = "round-988" },
			wager.Rejected, wager.ReferenceMismatch},
		{"amount before settlement", wager.Refund, 2500, wager.Bet, wager.Rejected, func(p *wager.ExternalParams) { p.Money = brl(t, 3000) },
			wager.Rejected, wager.ReferenceAmountMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			own := ""
			if tt.refKind == wager.Win {
				own = "origin-0"
			}
			p := params(t, "ref-1", tt.refKind, 2500, own)
			tt.mutate(&p)
			ref := stored(t, p, tt.refStatus)
			tx := pending(t, params(t, "tx-1", tt.kind, tt.minor, "ref-1"))
			w := holder(t, 100000)
			if _, err := rules(t).Apply(tx, w, &wager.Reference{Transaction: ref}, t1); err != nil {
				t.Fatal(err)
			}
			if tx.Status() != tt.status || tx.FailureCode() != tt.code {
				t.Fatalf("transaction = %s %s; want %s %s", tx.Status(), tx.FailureCode(), tt.status, tt.code)
			}
			if tt.status == wager.Rejected && (w.Balance() != brl(t, 100000) || w.Version() != 5) {
				t.Fatalf("wallet changed to %+v", w.Snapshot())
			}
		})
	}
}

func TestApplyReversalPolicy(t *testing.T) {
	tests := []struct {
		name     string
		kind     wager.Kind
		refKind  wager.Kind
		reversed bool
		status   wager.Status
		code     wager.FailureCode
	}{
		{"refund of a reversed bet", wager.Refund, wager.Bet, true, wager.Rejected, wager.AlreadyReversed},
		{"rollback of a refunded bet", wager.Rollback, wager.Bet, true, wager.Rejected, wager.AlreadyReversed},
		{"second rollback of a win", wager.Rollback, wager.Win, true, wager.Rejected, wager.AlreadyReversed},
		{"second rollback of a refund", wager.Rollback, wager.Refund, true, wager.Rejected, wager.AlreadyReversed},
		{"rollback of a refund", wager.Rollback, wager.Refund, false, wager.Processed, ""},
		{"win of a reversed bet", wager.Win, wager.Bet, true, wager.Processed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := referenced(t, "ref-1", tt.refKind, 2500, wager.Processed)
			tx := pending(t, params(t, "tx-1", tt.kind, 2500, "ref-1"))
			w := holder(t, 100000)
			out, err := rules(t).Apply(tx, w, &wager.Reference{Transaction: ref, Reversed: tt.reversed}, t1)
			if err != nil {
				t.Fatal(err)
			}
			if tx.Status() != tt.status || tx.FailureCode() != tt.code {
				t.Fatalf("transaction = %s %s; want %s %s", tx.Status(), tx.FailureCode(), tt.status, tt.code)
			}
			if tt.status == wager.Rejected && (out.Entry != nil || w.Version() != 5) {
				t.Fatalf("rejected reversal moved the wallet: %+v", w.Snapshot())
			}
		})
	}
}

func TestApplyResolvesReferenceOverTime(t *testing.T) {
	r := rules(t)
	tx := pending(t, params(t, "rollback-1", wager.Rollback, 5000, "win-1"))
	w := holder(t, 10000)

	out, err := r.Apply(tx, w, nil, t1)
	if err != nil {
		t.Fatal(err)
	}
	deadline := t1.Add(15 * time.Minute)
	s := tx.Snapshot()
	if s.Status != wager.PendingReference || s.Attempts != 1 || s.NextAttemptAt != t1.Add(time.Second) || s.ReferenceDeadlineAt != deadline {
		t.Fatalf("awaiting = %+v", s)
	}
	want := []events.Event{events.NewWagerTransactionPendingReference(events.Meta{
		EventID:       eventID(tx.ID(), events.TypeWagerTransactionPendingReference),
		CorrelationID: "corr-rollback-1",
		OccurredAt:    t1,
	}, events.WagerTransactionPendingReferenceData{
		TransactionData: events.TransactionData{
			TransactionID:                  tx.ID(),
			Origin:                         "EXTERNAL",
			ProviderID:                     "provider-a",
			ExternalTransactionID:          "rollback-1",
			WalletID:                       walletID,
			PlayerID:                       playerID,
			RoundID:                        "round-987",
			GameID:                         "fortune-chimp",
			Kind:                           "ROLLBACK",
			Money:                          brl(t, 5000),
			ReferenceExternalTransactionID: "win-1",
		},
		ReferenceDeadlineAt: deadline,
	})}
	if out.Entry != nil || !reflect.DeepEqual(out.Events, want) {
		t.Fatalf("outcome = %+v; want %+v", out, want)
	}

	t2 := t1.Add(time.Second)
	if out, err = r.Apply(tx, w, nil, t2); err != nil || out.Entry != nil || out.Events != nil {
		t.Fatalf("reschedule outcome = %+v, %v", out, err)
	}
	if s = tx.Snapshot(); s.Status != wager.PendingReference || s.Attempts != 2 || s.NextAttemptAt != t2.Add(2*time.Second) {
		t.Fatalf("rescheduled = %+v", s)
	}

	t3 := t2.Add(2 * time.Second)
	win := referenced(t, "win-1", wager.Win, 5000, wager.PendingReference)
	if out, err = r.Apply(tx, w, &wager.Reference{Transaction: win}, t3); err != nil || out.Events != nil {
		t.Fatalf("unsettled outcome = %+v, %v", out, err)
	}
	if s = tx.Snapshot(); s.Status != wager.PendingReference || s.Attempts != 3 || s.ReferenceTransactionID != win.ID() {
		t.Fatalf("waiting on an unsettled reference = %+v", s)
	}

	t4 := t3.Add(3 * time.Second)
	win = referenced(t, "win-1", wager.Win, 5000, wager.Processed)
	out, err = r.Apply(tx, w, &wager.Reference{Transaction: win}, t4)
	if err != nil {
		t.Fatal(err)
	}
	s = tx.Snapshot()
	if s.Status != wager.Processed || s.Attempts != 3 || s.CompletedAt != t4 || *s.Result != (wager.Result{Balance: brl(t, 5000), WalletVersion: 6}) {
		t.Fatalf("resolved = %+v", s)
	}
	if out.Entry == nil || out.Entry.Snapshot().Direction != ledger.Debit || !slices.Equal(eventTypes(out.Events), processedTypes) {
		t.Fatalf("resolved outcome = %+v", out)
	}
	if _, err := wager.Rehydrate(s); err != nil {
		t.Fatalf("resolved snapshot does not rehydrate: %v", err)
	}
}

func TestApplyReferenceExpiry(t *testing.T) {
	awaiting := func(t *testing.T, extID, ref string, kind wager.Kind) *wager.Transaction {
		return stored(t, params(t, extID, kind, 2500, ref), wager.PendingReference)
	}
	deadline := t0.Add(15 * time.Minute)
	tests := []struct {
		name        string
		maxAttempts int
		tx          func(*testing.T) *wager.Transaction
		ref         func(*testing.T) *wager.Transaction
		now         time.Time
		code        wager.FailureCode
	}{
		{"missing at the deadline", 20,
			func(t *testing.T) *wager.Transaction { return awaiting(t, "refund-1", "bet-1", wager.Refund) },
			nil, deadline, wager.ReferenceNotFound},
		{"unsettled at the deadline", 20,
			func(t *testing.T) *wager.Transaction { return awaiting(t, "rollback-1", "win-1", wager.Rollback) },
			func(t *testing.T) *wager.Transaction {
				return referenced(t, "win-1", wager.Win, 2500, wager.PendingReference)
			},
			deadline, wager.ReferenceNotSettled},
		{"missing after the last attempt", 2,
			func(t *testing.T) *wager.Transaction { return awaiting(t, "refund-1", "bet-1", wager.Refund) },
			nil, t1, wager.ReferenceNotFound},
		{"missing with a single attempt", 1,
			func(t *testing.T) *wager.Transaction {
				return pending(t, params(t, "refund-1", wager.Refund, 2500, "bet-1"))
			},
			nil, t1, wager.ReferenceNotFound},
		{"rejected while waiting", 20,
			func(t *testing.T) *wager.Transaction { return awaiting(t, "refund-1", "bet-1", wager.Refund) },
			func(t *testing.T) *wager.Transaction { return referenced(t, "bet-1", wager.Bet, 2500, wager.Rejected) },
			t1, wager.ReferenceNotProcessed},
		{"failed on arrival", 20,
			func(t *testing.T) *wager.Transaction {
				return pending(t, params(t, "refund-1", wager.Refund, 2500, "bet-1"))
			},
			func(t *testing.T) *wager.Transaction { return referenced(t, "bet-1", wager.Bet, 2500, wager.Failed) },
			t1, wager.ReferenceNotProcessed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := wager.NewRules(15*time.Minute, tt.maxAttempts, func(int) time.Duration { return time.Second })
			if err != nil {
				t.Fatal(err)
			}
			tx := tt.tx(t)
			var ref *wager.Reference
			if tt.ref != nil {
				ref = &wager.Reference{Transaction: tt.ref(t)}
			}
			w := holder(t, 10000)
			out, err := r.Apply(tx, w, ref, tt.now)
			if err != nil {
				t.Fatal(err)
			}
			s := tx.Snapshot()
			if s.Status != wager.Rejected || s.FailureCode != tt.code || s.CompletedAt != tt.now {
				t.Fatalf("transaction = %+v; want REJECTED %s", s, tt.code)
			}
			if s.Result == nil || *s.Result != (wager.Result{Balance: brl(t, 10000), WalletVersion: 5}) {
				t.Fatalf("result = %+v; want the current balance", s.Result)
			}
			if out.Entry != nil || !slices.Equal(eventTypes(out.Events), rejectedTypes) {
				t.Fatalf("outcome = %+v", out)
			}
			ev := out.Events[0].(events.WagerTransactionRejected)
			if ev.Data.FailureCode != string(tt.code) || ev.Data.Balance != brl(t, 10000) || ev.Data.WalletVersion != 5 {
				t.Fatalf("rejected event data = %+v", ev.Data)
			}
			if _, err := wager.Rehydrate(s); err != nil {
				t.Fatalf("expired snapshot does not rehydrate: %v", err)
			}
		})
	}
}

func TestApplySchedulesWithinDeadline(t *testing.T) {
	tests := []struct {
		name    string
		backoff time.Duration
		next    time.Time
	}{
		{"capped at the deadline", 10 * time.Second, t1.Add(5 * time.Second)},
		{"negative backoff", -5 * time.Second, t1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := wager.NewRules(5*time.Second, 20, func(int) time.Duration { return tt.backoff })
			if err != nil {
				t.Fatal(err)
			}
			tx := pending(t, params(t, "refund-1", wager.Refund, 2500, "bet-1"))
			if _, err := r.Apply(tx, holder(t, 10000), nil, t1); err != nil {
				t.Fatal(err)
			}
			if s := tx.Snapshot(); s.NextAttemptAt != tt.next || s.ReferenceDeadlineAt != t1.Add(5*time.Second) {
				t.Fatalf("next = %s, deadline = %s; want %s", s.NextAttemptAt, s.ReferenceDeadlineAt, tt.next)
			}
		})
	}
}

func TestApplyTwiceIsRejected(t *testing.T) {
	tx := pending(t, params(t, "bet-1", wager.Bet, 2500, ""))
	w := holder(t, 10000)
	if _, err := rules(t).Apply(tx, w, nil, t1); err != nil {
		t.Fatal(err)
	}
	before := w.Snapshot()
	if _, err := rules(t).Apply(tx, w, nil, t1); !errors.Is(err, domain.ErrTerminalState) {
		t.Fatalf("second Apply: err = %v; want ErrTerminalState", err)
	}
	if w.Snapshot() != before {
		t.Fatalf("second Apply moved the wallet to %+v", w.Snapshot())
	}
}

func TestApplyRejectsInvalidCalls(t *testing.T) {
	foreign, err := wallet.Rehydrate(wallet.Snapshot{
		ID: otherWalletID, PlayerID: playerID, Balance: brl(t, 10000), Version: 5, CreatedAt: t0, UpdatedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	bet := func(t *testing.T) *wager.Transaction { return pending(t, params(t, "bet-1", wager.Bet, 2500, "")) }
	refund := func(t *testing.T) *wager.Transaction {
		return pending(t, params(t, "refund-1", wager.Refund, 2500, "bet-1"))
	}
	linked := func(t *testing.T) *wager.Transaction {
		s := stored(t, params(t, "refund-1", wager.Refund, 2500, "bet-1"), wager.PendingReference).Snapshot()
		s.ReferenceTransactionID = txID("bet-0")
		tx, err := wager.Rehydrate(s)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	processedBet := func(t *testing.T) *wager.Reference {
		return &wager.Reference{Transaction: referenced(t, "bet-1", wager.Bet, 2500, wager.Processed)}
	}
	tests := []struct {
		name   string
		rules  func(*testing.T) wager.Rules
		tx     func(*testing.T) *wager.Transaction
		wallet *wallet.Wallet
		ref    func(*testing.T, *wager.Transaction) *wager.Reference
		now    time.Time
		want   error
		msg    string
	}{
		{"zero rules", func(*testing.T) wager.Rules { return wager.Rules{} }, bet, nil, nil, t1, domain.ErrUninitialized, ""},
		{"nil transaction", rules, func(*testing.T) *wager.Transaction { return nil }, nil, nil, t1, domain.ErrUninitialized, ""},
		{"zero transaction", rules, func(*testing.T) *wager.Transaction { return &wager.Transaction{} }, nil, nil, t1, domain.ErrUninitialized, ""},
		{"terminal transaction", rules, func(t *testing.T) *wager.Transaction {
			return stored(t, params(t, "bet-1", wager.Bet, 2500, ""), wager.Processed)
		}, nil, nil, t1, domain.ErrTerminalState, ""},
		{"missing time", rules, bet, nil, nil, time.Time{}, domain.ErrRequired, "now: REQUIRED"},
		{"another wallet", rules, bet, foreign, nil, t1, domain.ErrInvalidValue, "wallet: INVALID_VALUE"},
		{"zero wallet", rules, bet, &wallet.Wallet{}, nil, t1, domain.ErrInvalidValue, "wallet: INVALID_VALUE"},
		{"empty reference", rules, refund, nil, func(*testing.T, *wager.Transaction) *wager.Reference {
			return &wager.Reference{}
		}, t1, domain.ErrUninitialized, "reference: UNINITIALIZED"},
		{"reference for a bet", rules, bet, nil, func(t *testing.T, _ *wager.Transaction) *wager.Reference {
			return processedBet(t)
		}, t1, domain.ErrInvalidValue, "reference: INVALID_VALUE"},
		{"reference to itself", rules, refund, nil, func(_ *testing.T, tx *wager.Transaction) *wager.Reference {
			return &wager.Reference{Transaction: tx}
		}, t1, domain.ErrInvalidValue, "reference: INVALID_VALUE"},
		{"reference other than the linked one", rules, linked, nil, func(t *testing.T, _ *wager.Transaction) *wager.Reference {
			return processedBet(t)
		}, t1, domain.ErrInvalidValue, "reference: INVALID_VALUE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := tt.tx(t)
			var ref *wager.Reference
			if tt.ref != nil {
				ref = tt.ref(t, tx)
			}
			w := tt.wallet
			if w == nil {
				w = holder(t, 10000)
			}
			var before wager.Snapshot
			if tx != nil {
				before = tx.Snapshot()
			}
			walletBefore := w.Snapshot()
			out, err := tt.rules(t).Apply(tx, w, ref, tt.now)
			if !errors.Is(err, tt.want) || (tt.msg != "" && err.Error() != tt.msg) {
				t.Fatalf("err = %v; want %v %q", err, tt.want, tt.msg)
			}
			if out.Entry != nil || out.Events != nil {
				t.Fatalf("outcome = %+v; want none", out)
			}
			if tx != nil && !reflect.DeepEqual(tx.Snapshot(), before) {
				t.Fatalf("transaction changed to %+v", tx.Snapshot())
			}
			if w.Snapshot() != walletBefore {
				t.Fatalf("wallet changed to %+v", w.Snapshot())
			}
		})
	}
}

func TestNewRules(t *testing.T) {
	_, err := wager.NewRules(0, 0, nil)
	if want := "referenceTTL: INVALID_VALUE\nmaxAttempts: INVALID_VALUE\nbackoff: REQUIRED"; err == nil || err.Error() != want {
		t.Fatalf("NewRules(0, 0, nil) err = %v; want %q", err, want)
	}
	if _, err := wager.NewRules(-time.Second, 1, func(int) time.Duration { return 0 }); err == nil || err.Error() != "referenceTTL: INVALID_VALUE" {
		t.Fatalf("negative TTL: err = %v", err)
	}
}
