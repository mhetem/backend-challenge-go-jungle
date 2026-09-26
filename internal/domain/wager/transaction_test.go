package wager_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

func TestNewExternal(t *testing.T) {
	p := params(t, "rollback-1", wager.Rollback, 2500, "bet-1")
	tx, err := wager.NewExternal(p, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := wager.Snapshot{
		ID:                             p.ID,
		WalletID:                       walletID,
		PlayerID:                       playerID,
		Kind:                           wager.Rollback,
		Money:                          brl(t, 2500),
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "rollback-1",
		IdempotencyKey:                 "provider-a:rollback-1",
		PayloadHash:                    "hash-rollback-1",
		RoundID:                        "round-987",
		GameID:                         "fortune-chimp",
		ReferenceExternalTransactionID: "bet-1",
		CorrelationID:                  "corr-rollback-1",
		Status:                         wager.Pending,
		CreatedAt:                      t0,
		UpdatedAt:                      t0,
	}
	if got := tx.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %+v; want %+v", got, want)
	}
	if tx.ID() != p.ID || tx.WalletID() != walletID || tx.Kind() != wager.Rollback || tx.Status() != wager.Pending || tx.FailureCode() != "" {
		t.Fatalf("accessors disagree with %+v", want)
	}
	if tx.Kind().Origin() != wager.External {
		t.Fatalf("origin = %s; want EXTERNAL", tx.Kind().Origin())
	}
}

func TestNewExternalRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*wager.ExternalParams)
		want   string
	}{
		{"missing id", func(p *wager.ExternalParams) { p.ID = uuid.Nil }, "id: REQUIRED"},
		{"missing provider", func(p *wager.ExternalParams) { p.ProviderID = "" }, "providerId: REQUIRED"},
		{"missing external id", func(p *wager.ExternalParams) { p.ExternalTransactionID = "" }, "externalTransactionId: REQUIRED"},
		{"missing idempotency key", func(p *wager.ExternalParams) { p.IdempotencyKey = "" }, "idempotencyKey: REQUIRED"},
		{"missing payload hash", func(p *wager.ExternalParams) { p.PayloadHash = "" }, "payloadHash: REQUIRED"},
		{"missing wallet", func(p *wager.ExternalParams) { p.WalletID = uuid.Nil }, "walletId: REQUIRED"},
		{"missing player", func(p *wager.ExternalParams) { p.PlayerID = uuid.Nil }, "playerId: REQUIRED"},
		{"missing round", func(p *wager.ExternalParams) { p.RoundID = "" }, "roundId: REQUIRED"},
		{"missing game", func(p *wager.ExternalParams) { p.GameID = "" }, "gameId: REQUIRED"},
		{"missing correlation", func(p *wager.ExternalParams) { p.CorrelationID = "" }, "correlationId: REQUIRED"},
		{"opening", func(p *wager.ExternalParams) { p.Kind = wager.Opening }, "kind: KIND_NOT_ALLOWED"},
		{"unknown kind", func(p *wager.ExternalParams) { p.Kind = "JACKPOT" }, "kind: INVALID_VALUE"},
		{"lowercase kind", func(p *wager.ExternalParams) { p.Kind = "bet" }, "kind: INVALID_VALUE"},
		{"missing kind", func(p *wager.ExternalParams) { p.Kind = "" }, "kind: INVALID_VALUE"},
		{"uninitialized money", func(p *wager.ExternalParams) { p.Money = money.Money{} }, "money: REQUIRED"},
		{"negative money", func(p *wager.ExternalParams) { p.Money = brl(t, -2500) }, "money: INVALID_AMOUNT"},
		{"refund without reference", func(p *wager.ExternalParams) { p.Kind = wager.Refund }, "referenceExternalTransactionId: REQUIRED"},
		{"rollback without reference", func(p *wager.ExternalParams) { p.Kind = wager.Rollback }, "referenceExternalTransactionId: REQUIRED"},
		{"bet with reference", func(p *wager.ExternalParams) { p.ReferenceExternalTransactionID = "bet-0" }, "referenceExternalTransactionId: INVALID_VALUE"},
		{"loss with reference", func(p *wager.ExternalParams) {
			p.Kind, p.Money, p.ReferenceExternalTransactionID = wager.Loss, brl(t, 0), "bet-0"
		}, "referenceExternalTransactionId: INVALID_VALUE"},
		{"win referencing itself", func(p *wager.ExternalParams) {
			p.Kind, p.ReferenceExternalTransactionID = wager.Win, p.ExternalTransactionID
		}, "referenceExternalTransactionId: INVALID_VALUE"},
		{"refund referencing itself", func(p *wager.ExternalParams) {
			p.Kind, p.ReferenceExternalTransactionID = wager.Refund, p.ExternalTransactionID
		}, "referenceExternalTransactionId: INVALID_VALUE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := params(t, "bet-1", wager.Bet, 2500, "")
			tt.mutate(&p)
			tx, err := wager.NewExternal(p, t0)
			if err == nil || err.Error() != tt.want || tx != nil {
				t.Fatalf("NewExternal = %v, %v; want error %q", tx, err, tt.want)
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Category != domain.Invalid {
				t.Fatalf("error %v is not a domain Invalid error", err)
			}
		})
	}
}

func TestNewExternalReportsEveryField(t *testing.T) {
	_, err := wager.NewExternal(wager.ExternalParams{}, time.Time{})
	want := "id: REQUIRED\nproviderId: REQUIRED\nexternalTransactionId: REQUIRED\nplayerId: REQUIRED\n" +
		"walletId: REQUIRED\nroundId: REQUIRED\ngameId: REQUIRED\nkind: INVALID_VALUE\nmoney: REQUIRED\n" +
		"idempotencyKey: REQUIRED\npayloadHash: REQUIRED\ncorrelationId: REQUIRED\ncreatedAt: REQUIRED"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v; want %q", err, want)
	}
}

func TestZeroPolicy(t *testing.T) {
	tests := []struct {
		kind  wager.Kind
		minor int64
		ok    bool
	}{
		{wager.Bet, 0, false},
		{wager.Bet, 1, true},
		{wager.Win, 0, false},
		{wager.Win, 1, true},
		{wager.Loss, 0, true},
		{wager.Loss, 1, false},
		{wager.Refund, 0, false},
		{wager.Refund, 1, true},
		{wager.Rollback, 0, false},
		{wager.Rollback, 1, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind)+"/"+brl(t, tt.minor).String(), func(t *testing.T) {
			ref := ""
			if tt.kind.Reversal() {
				ref = "bet-0"
			}
			_, err := wager.NewExternal(params(t, "tx-1", tt.kind, tt.minor, ref), t0)
			if tt.ok != (err == nil) {
				t.Fatalf("err = %v; want ok=%v", err, tt.ok)
			}
			if !tt.ok && err.Error() != "money: INVALID_AMOUNT" {
				t.Fatalf("err = %q; want money: INVALID_AMOUNT", err)
			}
		})
	}
}

func TestNewOpening(t *testing.T) {
	tx, err := wager.NewOpening(walletID, playerID, brl(t, 100000), "corr-open", t0)
	if err != nil {
		t.Fatal(err)
	}
	want := wager.Snapshot{
		ID:            wallet.OpeningTransactionID(walletID),
		WalletID:      walletID,
		PlayerID:      playerID,
		Kind:          wager.Opening,
		Money:         brl(t, 100000),
		CorrelationID: "corr-open",
		Status:        wager.Processed,
		Result:        &wager.Result{Balance: brl(t, 100000), WalletVersion: 1},
		CreatedAt:     t0,
		UpdatedAt:     t0,
		CompletedAt:   t0,
	}
	if got := tx.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %+v; want %+v", got, want)
	}
	if tx.Kind().Origin() != wager.Internal {
		t.Fatalf("origin = %s; want INTERNAL", tx.Kind().Origin())
	}
	again, err := wager.NewOpening(walletID, playerID, brl(t, 5000), "corr-other", t1)
	if err != nil || again.ID() != tx.ID() {
		t.Fatalf("second opening id = %v, %v; want the deterministic %s", again, err, tx.ID())
	}
	other, err := wager.NewOpening(otherWalletID, playerID, brl(t, 100000), "corr-open", t0)
	if err != nil || other.ID() == tx.ID() {
		t.Fatalf("openings of two wallets share id %s", tx.ID())
	}
}

func TestNewOpeningRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name          string
		walletID      uuid.UUID
		playerID      uuid.UUID
		amount        money.Money
		correlationID string
		now           time.Time
		want          string
	}{
		{"zero amount", walletID, playerID, brl(t, 0), "corr", t0, "money: INVALID_AMOUNT"},
		{"negative amount", walletID, playerID, brl(t, -1), "corr", t0, "money: INVALID_AMOUNT"},
		{"everything missing", uuid.Nil, uuid.Nil, money.Money{}, "", time.Time{},
			"walletId: REQUIRED\nplayerId: REQUIRED\nmoney: REQUIRED\ncorrelationId: REQUIRED\ncreatedAt: REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := wager.NewOpening(tt.walletID, tt.playerID, tt.amount, tt.correlationID, tt.now)
			if err == nil || err.Error() != tt.want || tx != nil {
				t.Fatalf("NewOpening = %v, %v; want error %q", tx, err, tt.want)
			}
		})
	}
}

func opening(t *testing.T) wager.Snapshot {
	t.Helper()
	tx, err := wager.NewOpening(walletID, playerID, brl(t, 100000), "corr-open", t0)
	if err != nil {
		t.Fatal(err)
	}
	return tx.Snapshot()
}

func processedRefund(t *testing.T) wager.Snapshot {
	t.Helper()
	s := stored(t, params(t, "refund-1", wager.Refund, 2500, "bet-1"), wager.Processed).Snapshot()
	s.ReferenceTransactionID = txID("bet-1")
	return s
}

func TestRehydrateRoundTrip(t *testing.T) {
	rejected := stored(t, params(t, "refund-2", wager.Refund, 2500, "bet-1"), wager.Rejected).Snapshot()
	rejected.Result = &wager.Result{Balance: brl(t, 0), WalletVersion: 3}
	snapshots := map[string]wager.Snapshot{
		"processed refund":  processedRefund(t),
		"processed loss":    stored(t, params(t, "loss-1", wager.Loss, 0, ""), wager.Processed).Snapshot(),
		"pending reference": stored(t, params(t, "win-1", wager.Win, 2500, "bet-1"), wager.PendingReference).Snapshot(),
		"rejected":          rejected,
		"failed":            stored(t, params(t, "bet-2", wager.Bet, 2500, ""), wager.Failed).Snapshot(),
		"opening":           opening(t),
	}
	for name, s := range snapshots {
		t.Run(name, func(t *testing.T) {
			tx, err := wager.Rehydrate(s)
			if err != nil || !reflect.DeepEqual(tx.Snapshot(), s) {
				t.Fatalf("Rehydrate = %+v, %v; want %+v", tx, err, s)
			}
		})
	}
}

func TestSnapshotsAreCopies(t *testing.T) {
	s := processedRefund(t)
	tx, err := wager.Rehydrate(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Result.WalletVersion = 99
	out := tx.Snapshot()
	out.Result.WalletVersion = 42
	if got := tx.Snapshot().Result.WalletVersion; got != 2 {
		t.Fatalf("stored result version = %d; want 2", got)
	}
}

func TestRehydrateRejectsInvalidSnapshots(t *testing.T) {
	usd := amount(t, 2500, money.USD)
	tests := []struct {
		name   string
		base   func(*testing.T) wager.Snapshot
		mutate func(*wager.Snapshot)
		want   string
	}{
		{"missing ids", processedRefund, func(s *wager.Snapshot) {
			s.ID, s.WalletID, s.PlayerID = uuid.Nil, uuid.Nil, uuid.Nil
		}, "id: REQUIRED\nwalletId: REQUIRED\nplayerId: REQUIRED"},
		{"unknown kind", processedRefund, func(s *wager.Snapshot) { s.Kind = "JACKPOT" },
			"kind: INVALID_VALUE\nreferenceExternalTransactionId: INVALID_VALUE"},
		{"loss with amount", processedRefund, func(s *wager.Snapshot) { s.Kind = wager.Loss },
			"money: INVALID_AMOUNT\nreferenceExternalTransactionId: INVALID_VALUE"},
		{"missing correlation", processedRefund, func(s *wager.Snapshot) { s.CorrelationID = "" }, "correlationId: REQUIRED"},
		{"missing timestamps", processedRefund, func(s *wager.Snapshot) { s.CreatedAt, s.UpdatedAt = time.Time{}, time.Time{} },
			"createdAt: REQUIRED\nupdatedAt: REQUIRED"},
		{"negative attempts", processedRefund, func(s *wager.Snapshot) { s.Attempts = -1 }, "attempts: INVALID_VALUE"},
		{"missing provider", processedRefund, func(s *wager.Snapshot) { s.ProviderID = "" }, "providerId: REQUIRED"},
		{"missing external metadata", processedRefund, func(s *wager.Snapshot) {
			s.ExternalTransactionID, s.IdempotencyKey, s.PayloadHash, s.RoundID, s.GameID = "", "", "", "", ""
		}, "externalTransactionId: REQUIRED\nidempotencyKey: REQUIRED\npayloadHash: REQUIRED\nroundId: REQUIRED\ngameId: REQUIRED"},
		{"missing reference", processedRefund, func(s *wager.Snapshot) { s.ReferenceExternalTransactionID = "" },
			"referenceExternalTransactionId: REQUIRED\nreferenceTransactionId: INVALID_VALUE"},
		{"pending is never stored", processedRefund, func(s *wager.Snapshot) {
			s.Status, s.Result, s.CompletedAt = wager.Pending, nil, time.Time{}
		}, "status: INVALID_VALUE"},
		{"unknown status", processedRefund, func(s *wager.Snapshot) { s.Status = "DONE" },
			"status: INVALID_VALUE\nresult: INVALID_VALUE\ncompletedAt: INVALID_VALUE"},
		{"processed without result", processedRefund, func(s *wager.Snapshot) { s.Result = nil }, "result: REQUIRED"},
		{"processed with failure code", processedRefund, func(s *wager.Snapshot) { s.FailureCode = wager.InsufficientFunds },
			"failureCode: INVALID_VALUE"},
		{"rejected without failure code", processedRefund, func(s *wager.Snapshot) { s.Status = wager.Rejected },
			"failureCode: INVALID_VALUE"},
		{"rejected as processing failure", processedRefund, func(s *wager.Snapshot) {
			s.Status, s.FailureCode = wager.Rejected, wager.ProcessingFailed
		}, "failureCode: INVALID_VALUE"},
		{"failed with rejection code", processedRefund, func(s *wager.Snapshot) {
			s.Status, s.FailureCode, s.Result = wager.Failed, wager.InsufficientFunds, nil
		}, "failureCode: INVALID_VALUE"},
		{"failed with result", processedRefund, func(s *wager.Snapshot) {
			s.Status, s.FailureCode = wager.Failed, wager.ProcessingFailed
		}, "result: INVALID_VALUE"},
		{"result in another currency", processedRefund, func(s *wager.Snapshot) {
			s.Result = &wager.Result{Balance: usd, WalletVersion: 2}
		}, "result.balance: INVALID_VALUE"},
		{"negative result", processedRefund, func(s *wager.Snapshot) {
			s.Result = &wager.Result{Balance: brl(t, -1), WalletVersion: 2}
		}, "result.balance: INVALID_AMOUNT"},
		{"uninitialized result", processedRefund, func(s *wager.Snapshot) {
			s.Result = &wager.Result{WalletVersion: 2}
		}, "result.balance: REQUIRED"},
		{"result version zero", processedRefund, func(s *wager.Snapshot) {
			s.Result = &wager.Result{Balance: brl(t, 0), WalletVersion: 0}
		}, "result.walletVersion: INVALID_VALUE"},
		{"terminal without completion", processedRefund, func(s *wager.Snapshot) { s.CompletedAt = time.Time{} },
			"completedAt: INVALID_VALUE"},
		{"pending reference without schedule", processedRefund, func(s *wager.Snapshot) {
			s.Status, s.Result, s.CompletedAt = wager.PendingReference, nil, time.Time{}
		}, "attempts: INVALID_VALUE\nnextAttemptAt: REQUIRED\nreferenceDeadlineAt: REQUIRED"},
		{"pending reference with completion", processedRefund, func(s *wager.Snapshot) {
			s.Status, s.Result = wager.PendingReference, nil
			s.Attempts, s.NextAttemptAt, s.ReferenceDeadlineAt = 1, t1, t1
		}, "completedAt: INVALID_VALUE"},
		{"opening with external metadata", opening, func(s *wager.Snapshot) { s.ProviderID = "provider-a" },
			"origin: INVALID_VALUE"},
		{"opening with a reference", opening, func(s *wager.Snapshot) { s.ReferenceTransactionID = txID("bet-1") },
			"origin: INVALID_VALUE"},
		{"opening with a foreign id", opening, func(s *wager.Snapshot) { s.ID = txID("opening") }, "id: INVALID_VALUE"},
		{"opening with zero amount", opening, func(s *wager.Snapshot) { s.Money = brl(t, 0) }, "money: INVALID_AMOUNT"},
		{"opening not processed", opening, func(s *wager.Snapshot) {
			s.Status, s.FailureCode = wager.Rejected, wager.InsufficientFunds
		}, "status: INVALID_VALUE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.base(t)
			tt.mutate(&s)
			tx, err := wager.Rehydrate(s)
			if err == nil || err.Error() != tt.want || tx != nil {
				t.Fatalf("Rehydrate = %v, %v; want error %q", tx, err, tt.want)
			}
		})
	}
}

func inStatus(t *testing.T, status wager.Status) *wager.Transaction {
	t.Helper()
	p := params(t, "refund-1", wager.Refund, 2500, "bet-1")
	if status == wager.Pending {
		return pending(t, p)
	}
	return stored(t, p, status)
}

func TestTransitions(t *testing.T) {
	result := wager.Result{Balance: brl(t, 7500), WalletVersion: 6}
	ops := map[string]func(*wager.Transaction) error{
		"MarkProcessed": func(tx *wager.Transaction) error { return tx.MarkProcessed(result, t1) },
		"Reject":        func(tx *wager.Transaction) error { return tx.Reject(wager.InsufficientFunds, nil, t1) },
		"Fail":          func(tx *wager.Transaction) error { return tx.Fail(wager.ProcessingFailed, t1) },
		"AwaitReference": func(tx *wager.Transaction) error {
			return tx.AwaitReference(t1.Add(time.Second), t1.Add(time.Hour), t1)
		},
		"Reschedule":       func(tx *wager.Transaction) error { return tx.Reschedule(t1.Add(time.Second), t1) },
		"ResumeProcessing": func(tx *wager.Transaction) error { return tx.ResumeProcessing(t1) },
	}
	tests := []struct {
		from wager.Status
		op   string
		to   wager.Status
		err  error
	}{
		{wager.Pending, "MarkProcessed", wager.Processed, nil},
		{wager.Pending, "Reject", wager.Rejected, nil},
		{wager.Pending, "Fail", wager.Failed, nil},
		{wager.Pending, "AwaitReference", wager.PendingReference, nil},
		{wager.Pending, "Reschedule", "", domain.ErrInvalidTransition},
		{wager.Pending, "ResumeProcessing", "", domain.ErrInvalidTransition},
		{wager.PendingReference, "MarkProcessed", "", domain.ErrInvalidTransition},
		{wager.PendingReference, "Reject", wager.Rejected, nil},
		{wager.PendingReference, "Fail", wager.Failed, nil},
		{wager.PendingReference, "AwaitReference", "", domain.ErrInvalidTransition},
		{wager.PendingReference, "Reschedule", wager.PendingReference, nil},
		{wager.PendingReference, "ResumeProcessing", wager.Pending, nil},
		{wager.Processed, "MarkProcessed", "", domain.ErrTerminalState},
		{wager.Processed, "Reject", "", domain.ErrTerminalState},
		{wager.Processed, "Fail", "", domain.ErrTerminalState},
		{wager.Processed, "AwaitReference", "", domain.ErrTerminalState},
		{wager.Processed, "Reschedule", "", domain.ErrTerminalState},
		{wager.Processed, "ResumeProcessing", "", domain.ErrTerminalState},
		{wager.Rejected, "MarkProcessed", "", domain.ErrTerminalState},
		{wager.Rejected, "Reject", "", domain.ErrTerminalState},
		{wager.Rejected, "Fail", "", domain.ErrTerminalState},
		{wager.Rejected, "AwaitReference", "", domain.ErrTerminalState},
		{wager.Rejected, "Reschedule", "", domain.ErrTerminalState},
		{wager.Rejected, "ResumeProcessing", "", domain.ErrTerminalState},
		{wager.Failed, "MarkProcessed", "", domain.ErrTerminalState},
		{wager.Failed, "Reject", "", domain.ErrTerminalState},
		{wager.Failed, "Fail", "", domain.ErrTerminalState},
		{wager.Failed, "AwaitReference", "", domain.ErrTerminalState},
		{wager.Failed, "Reschedule", "", domain.ErrTerminalState},
		{wager.Failed, "ResumeProcessing", "", domain.ErrTerminalState},
	}
	for _, tt := range tests {
		t.Run(string(tt.from)+"/"+tt.op, func(t *testing.T) {
			tx := inStatus(t, tt.from)
			before := tx.Snapshot()
			err := ops[tt.op](tx)
			if tt.err != nil {
				if !errors.Is(err, tt.err) || !reflect.DeepEqual(tx.Snapshot(), before) {
					t.Fatalf("err = %v, snapshot %+v; want %v and no change", err, tx.Snapshot(), tt.err)
				}
				return
			}
			if err != nil || tx.Status() != tt.to {
				t.Fatalf("status = %s, err = %v; want %s", tx.Status(), err, tt.to)
			}
			if _, err := wager.Rehydrate(tx.Snapshot()); tt.to != wager.Pending && err != nil {
				t.Fatalf("resulting snapshot does not rehydrate: %v", err)
			}
		})
	}
}

func TestTerminalTransitions(t *testing.T) {
	result := wager.Result{Balance: brl(t, 7500), WalletVersion: 6}

	processed := pending(t, params(t, "bet-1", wager.Bet, 2500, ""))
	if err := processed.MarkProcessed(result, t1); err != nil {
		t.Fatal(err)
	}
	s := processed.Snapshot()
	if s.Status != wager.Processed || *s.Result != result || s.FailureCode != "" || s.UpdatedAt != t1 || s.CompletedAt != t1 || s.CreatedAt != t0 {
		t.Fatalf("processed = %+v", s)
	}

	rejected := pending(t, params(t, "bet-2", wager.Bet, 2500, ""))
	given := result
	if err := rejected.Reject(wager.InsufficientFunds, &given, t1); err != nil {
		t.Fatal(err)
	}
	given.WalletVersion = 99
	s = rejected.Snapshot()
	if s.Status != wager.Rejected || s.FailureCode != wager.InsufficientFunds || *s.Result != result || s.CompletedAt != t1 {
		t.Fatalf("rejected = %+v", s)
	}

	unresolved := pending(t, params(t, "bet-3", wager.Bet, 2500, ""))
	if err := unresolved.Reject(wager.WalletNotFound, nil, t1); err != nil {
		t.Fatal(err)
	}
	if s = unresolved.Snapshot(); s.Result != nil || s.FailureCode != wager.WalletNotFound {
		t.Fatalf("rejected without result = %+v", s)
	}

	failed := pending(t, params(t, "bet-4", wager.Bet, 2500, ""))
	if err := failed.Fail(wager.ProcessingFailed, t1); err != nil {
		t.Fatal(err)
	}
	if s = failed.Snapshot(); s.Status != wager.Failed || s.FailureCode != wager.ProcessingFailed || s.Result != nil || s.CompletedAt != t1 {
		t.Fatalf("failed = %+v", s)
	}
}

func TestReferenceLifecycle(t *testing.T) {
	tx := pending(t, params(t, "refund-1", wager.Refund, 2500, "bet-1"))
	deadline := t0.Add(15 * time.Minute)
	if err := tx.AwaitReference(t0.Add(time.Second), deadline, t0); err != nil {
		t.Fatal(err)
	}
	s := tx.Snapshot()
	if s.Status != wager.PendingReference || s.Attempts != 1 || s.NextAttemptAt != t0.Add(time.Second) || s.ReferenceDeadlineAt != deadline || !s.CompletedAt.IsZero() {
		t.Fatalf("awaiting = %+v", s)
	}
	if err := tx.Reschedule(t1.Add(2*time.Second), t1); err != nil {
		t.Fatal(err)
	}
	s = tx.Snapshot()
	if s.Status != wager.PendingReference || s.Attempts != 2 || s.NextAttemptAt != t1.Add(2*time.Second) || s.ReferenceDeadlineAt != deadline || s.UpdatedAt != t1 {
		t.Fatalf("rescheduled = %+v", s)
	}
	t2 := t1.Add(time.Minute)
	if err := tx.ResumeProcessing(t2); err != nil {
		t.Fatal(err)
	}
	s = tx.Snapshot()
	if s.Status != wager.Pending || s.Attempts != 2 || s.UpdatedAt != t2 {
		t.Fatalf("resumed = %+v", s)
	}
	if err := tx.MarkProcessed(wager.Result{Balance: brl(t, 12500), WalletVersion: 6}, t2); err != nil {
		t.Fatal(err)
	}
	if _, err := wager.Rehydrate(tx.Snapshot()); err != nil {
		t.Fatalf("processed snapshot does not rehydrate: %v", err)
	}
}

func TestTransitionArguments(t *testing.T) {
	usd := amount(t, 7500, money.USD)
	bet := func(t *testing.T) *wager.Transaction { return pending(t, params(t, "bet-1", wager.Bet, 2500, "")) }
	awaiting := func(t *testing.T) *wager.Transaction { return inStatus(t, wager.PendingReference) }
	refund := func(t *testing.T) *wager.Transaction { return inStatus(t, wager.Pending) }
	tests := []struct {
		name string
		tx   func(*testing.T) *wager.Transaction
		op   func(*wager.Transaction) error
		want error
		msg  string
	}{
		{"processed with uninitialized balance", bet, func(tx *wager.Transaction) error {
			return tx.MarkProcessed(wager.Result{WalletVersion: 6}, t1)
		}, domain.ErrRequired, "result.balance: REQUIRED"},
		{"processed with negative balance", bet, func(tx *wager.Transaction) error {
			return tx.MarkProcessed(wager.Result{Balance: brl(t, -1), WalletVersion: 6}, t1)
		}, domain.ErrInvalidAmount, "result.balance: INVALID_AMOUNT"},
		{"processed in another currency", bet, func(tx *wager.Transaction) error {
			return tx.MarkProcessed(wager.Result{Balance: usd, WalletVersion: 6}, t1)
		}, domain.ErrInvalidValue, "result.balance: INVALID_VALUE"},
		{"processed at version zero", bet, func(tx *wager.Transaction) error {
			return tx.MarkProcessed(wager.Result{Balance: brl(t, 0)}, t1)
		}, domain.ErrInvalidValue, "result.walletVersion: INVALID_VALUE"},
		{"rejected without code", bet, func(tx *wager.Transaction) error {
			return tx.Reject("", nil, t1)
		}, domain.ErrInvalidValue, "failureCode: INVALID_VALUE"},
		{"rejected with unknown code", bet, func(tx *wager.Transaction) error {
			return tx.Reject("NOPE", nil, t1)
		}, domain.ErrInvalidValue, "failureCode: INVALID_VALUE"},
		{"rejected as processing failure", bet, func(tx *wager.Transaction) error {
			return tx.Reject(wager.ProcessingFailed, nil, t1)
		}, domain.ErrInvalidValue, "failureCode: INVALID_VALUE"},
		{"rejected with invalid result", bet, func(tx *wager.Transaction) error {
			return tx.Reject(wager.InsufficientFunds, &wager.Result{Balance: brl(t, 0)}, t1)
		}, domain.ErrInvalidValue, "result.walletVersion: INVALID_VALUE"},
		{"failed with rejection code", bet, func(tx *wager.Transaction) error {
			return tx.Fail(wager.InsufficientFunds, t1)
		}, domain.ErrInvalidValue, "failureCode: INVALID_VALUE"},
		{"await with next attempt in the past", refund, func(tx *wager.Transaction) error {
			return tx.AwaitReference(t0, t1.Add(time.Hour), t1)
		}, domain.ErrInvalidValue, "nextAttemptAt: INVALID_VALUE"},
		{"await with expired deadline", refund, func(tx *wager.Transaction) error {
			return tx.AwaitReference(t1, t1, t1)
		}, domain.ErrInvalidValue, "referenceDeadlineAt: INVALID_VALUE"},
		{"await without a reference", bet, func(tx *wager.Transaction) error {
			return tx.AwaitReference(t1, t1.Add(time.Hour), t1)
		}, domain.ErrInvalidTransition, ""},
		{"reschedule into the past", awaiting, func(tx *wager.Transaction) error {
			return tx.Reschedule(t0, t1)
		}, domain.ErrInvalidValue, "nextAttemptAt: INVALID_VALUE"},
		{"processed without time", bet, func(tx *wager.Transaction) error {
			return tx.MarkProcessed(wager.Result{Balance: brl(t, 0), WalletVersion: 6}, time.Time{})
		}, domain.ErrRequired, "now: REQUIRED"},
		{"rejected without time", bet, func(tx *wager.Transaction) error {
			return tx.Reject(wager.InsufficientFunds, nil, time.Time{})
		}, domain.ErrRequired, "now: REQUIRED"},
		{"failed without time", bet, func(tx *wager.Transaction) error {
			return tx.Fail(wager.ProcessingFailed, time.Time{})
		}, domain.ErrRequired, "now: REQUIRED"},
		{"await without time", refund, func(tx *wager.Transaction) error {
			return tx.AwaitReference(t1, t1.Add(time.Hour), time.Time{})
		}, domain.ErrRequired, "now: REQUIRED"},
		{"reschedule without time", awaiting, func(tx *wager.Transaction) error {
			return tx.Reschedule(t1, time.Time{})
		}, domain.ErrRequired, "now: REQUIRED"},
		{"resume without time", awaiting, func(tx *wager.Transaction) error {
			return tx.ResumeProcessing(time.Time{})
		}, domain.ErrRequired, "now: REQUIRED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := tt.tx(t)
			before := tx.Snapshot()
			err := tt.op(tx)
			if !errors.Is(err, tt.want) || (tt.msg != "" && err.Error() != tt.msg) {
				t.Fatalf("err = %v; want %v %q", err, tt.want, tt.msg)
			}
			if !reflect.DeepEqual(tx.Snapshot(), before) {
				t.Fatalf("snapshot changed from %+v to %+v", before, tx.Snapshot())
			}
		})
	}
}

func TestUninitializedTransaction(t *testing.T) {
	result := wager.Result{Balance: brl(t, 0), WalletVersion: 1}
	for name, tx := range map[string]*wager.Transaction{"nil": nil, "zero": {}} {
		errs := map[string]error{
			"MarkProcessed":    tx.MarkProcessed(result, t1),
			"Reject":           tx.Reject(wager.InsufficientFunds, nil, t1),
			"Fail":             tx.Fail(wager.ProcessingFailed, t1),
			"AwaitReference":   tx.AwaitReference(t1, t1.Add(time.Hour), t1),
			"Reschedule":       tx.Reschedule(t1, t1),
			"ResumeProcessing": tx.ResumeProcessing(t1),
		}
		for op, err := range errs {
			if !errors.Is(err, domain.ErrUninitialized) {
				t.Errorf("%s transaction: %s err = %v; want ErrUninitialized", name, op, err)
			}
		}
	}
}

func TestFailureCodes(t *testing.T) {
	rejections := []wager.FailureCode{
		wager.InsufficientFunds, wager.ReversalInsufficientFunds, wager.WalletNotFound, wager.WalletPlayerMismatch,
		wager.CurrencyMismatch, wager.ReferenceNotFound, wager.ReferenceNotSettled, wager.ReferenceNotProcessed,
		wager.ReferenceMismatch, wager.ReferenceKindNotReversible, wager.ReferenceAmountMismatch, wager.AlreadyReversed,
		wager.BalanceOverflow,
	}
	correctable := map[wager.FailureCode]bool{
		wager.WalletNotFound: true, wager.WalletPlayerMismatch: true, wager.CurrencyMismatch: true,
		wager.ReferenceNotFound: true, wager.ReferenceMismatch: true, wager.ReferenceKindNotReversible: true,
		wager.ReferenceAmountMismatch: true,
	}
	for _, c := range rejections {
		if !c.Valid() || !c.Rejection() || c.Correctable() != correctable[c] {
			t.Errorf("%s: valid=%v rejection=%v correctable=%v", c, c.Valid(), c.Rejection(), c.Correctable())
		}
	}
	if c := wager.ProcessingFailed; !c.Valid() || c.Rejection() || c.Correctable() {
		t.Errorf("%s: valid=%v rejection=%v correctable=%v", c, c.Valid(), c.Rejection(), c.Correctable())
	}
	if c := wager.FailureCode("NOPE"); c.Valid() || c.Rejection() || c.Correctable() {
		t.Errorf("unknown code classified as valid")
	}
}
