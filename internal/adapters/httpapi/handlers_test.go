package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
)

var (
	t0       = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	walletID = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
	playerID = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	txID     = uuid.MustParse("0192f298-345e-7e38-af88-e43f851a819d")
)

type tokens struct{}

func (tokens) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "provider-a", "provider-b":
		return auth.Principal{ClientID: token, ProviderID: token, Roles: []auth.Role{auth.WagerProvider}}, nil
	case "operator":
		return auth.Principal{ClientID: "wallet-backoffice", Roles: []auth.Role{auth.WalletOperator}}, nil
	case "no-role":
		return auth.Principal{ClientID: "no-role-client", ProviderID: "provider-a"}, nil
	}
	return auth.Principal{}, fmt.Errorf("%w: unknown test token", auth.ErrUnauthenticated)
}

type fakeWallets struct {
	calls     int
	open      func(app.OpenWallet) (wallet.Snapshot, error)
	get       func(uuid.UUID) (wallet.Snapshot, error)
	ledger    func(uuid.UUID, string, int) (app.LedgerPage, error)
	reconcile func(uuid.UUID) (app.Reconciliation, error)
}

func (f *fakeWallets) Open(_ context.Context, cmd app.OpenWallet) (wallet.Snapshot, error) {
	f.calls++
	return f.open(cmd)
}

func (f *fakeWallets) Get(_ context.Context, id uuid.UUID) (wallet.Snapshot, error) {
	f.calls++
	return f.get(id)
}

func (f *fakeWallets) Ledger(_ context.Context, id uuid.UUID, cursor string, limit int) (app.LedgerPage, error) {
	f.calls++
	return f.ledger(id, cursor, limit)
}

func (f *fakeWallets) Reconcile(_ context.Context, id uuid.UUID) (app.Reconciliation, error) {
	f.calls++
	return f.reconcile(id)
}

type fakeWagers struct {
	calls    int
	submit   func(app.SubmitWager) (app.WagerResult, error)
	get      func(uuid.UUID) (wager.Snapshot, error)
	external func(string, string) (wager.Snapshot, error)
}

func (f *fakeWagers) Submit(_ context.Context, cmd app.SubmitWager) (app.WagerResult, error) {
	f.calls++
	return f.submit(cmd)
}

func (f *fakeWagers) Get(_ context.Context, id uuid.UUID) (wager.Snapshot, error) {
	f.calls++
	return f.get(id)
}

func (f *fakeWagers) GetByExternalID(_ context.Context, providerID, externalID string) (wager.Snapshot, error) {
	f.calls++
	return f.external(providerID, externalID)
}

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func walletSnapshot(t *testing.T) wallet.Snapshot {
	return wallet.Snapshot{ID: walletID, PlayerID: playerID, Balance: brl(t, "1000.00"), Version: 1, CreatedAt: t0, UpdatedAt: t0}
}

func betSnapshot(t *testing.T, status wager.Status) wager.Snapshot {
	s := wager.Snapshot{
		ID:                    txID,
		WalletID:              walletID,
		PlayerID:              playerID,
		Kind:                  wager.Bet,
		Money:                 brl(t, "25.00"),
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		CorrelationID:         "corr-1",
		Status:                status,
		CreatedAt:             t0,
		UpdatedAt:             t0,
	}
	switch status {
	case wager.Processed:
		s.Result, s.CompletedAt = &wager.Result{Balance: brl(t, "975.00"), WalletVersion: 2}, t0
	case wager.Rejected:
		s.FailureCode, s.Result, s.CompletedAt = wager.InsufficientFunds, &wager.Result{Balance: brl(t, "10.00"), WalletVersion: 7}, t0
	case wager.Failed:
		s.FailureCode, s.CompletedAt = wager.ProcessingFailed, t0
	case wager.PendingReference:
		s.Kind, s.ReferenceExternalTransactionID = wager.Refund, "transaction-122"
		s.Attempts, s.NextAttemptAt, s.ReferenceDeadlineAt = 1, t0.Add(time.Second), t0.Add(15*time.Minute)
	}
	return s
}

const betBody = `{
	"providerId": "provider-a",
	"externalTransactionId": "transaction-123",
	"playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
	"walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
	"roundId": "round-987",
	"gameId": "fortune-chimp",
	"kind": "BET",
	"money": {"amount": "25.00", "currency": "BRL"}
}`

type call struct {
	method      string
	path        string
	token       string
	body        string
	contentType string
	headers     map[string]string
}

type reply struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func serve(t *testing.T, wallets *fakeWallets, wagers *fakeWagers, c call) reply {
	t.Helper()
	discard := slog.New(slog.DiscardHandler)
	h, err := newHandler(health.NewChecker(nil, discard), tokens{}, wallets, wagers, prometheus.NewRegistry(), discard)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.body != "" {
		ct := c.contentType
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r := reply{status: rec.Code, header: rec.Header(), raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &r.body)
	return r
}

func submit(body, token string, headers map[string]string) call {
	h := map[string]string{"Idempotency-Key": "provider-a:transaction-123"}
	for k, v := range headers {
		h[k] = v
	}
	return call{method: http.MethodPost, path: "/wagering/transactions", token: token, body: body, headers: h}
}

func (r reply) requireProblem(t *testing.T, status int, code string, issues ...issue) {
	t.Helper()
	if r.status != status || r.body["code"] != code || r.header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %s; want %d problem %s", r.status, r.raw, status, code)
	}
	want := []any{}
	for _, i := range issues {
		want = append(want, map[string]any{"field": i.Field, "code": i.Code})
	}
	if got := r.body["errors"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("errors = %v; want %v", got, want)
	}
	if retryable := status == http.StatusServiceUnavailable; r.body["retryable"] != retryable || (r.header.Get("Retry-After") != "") != retryable {
		t.Fatalf("retryable = %v, Retry-After %q; want retryable %v", r.body["retryable"], r.header.Get("Retry-After"), retryable)
	}
}

func TestSubmitOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		result   app.WagerResult
		status   int
		location bool
		fields   map[string]any
	}{
		{"processed", app.WagerResult{Transaction: betSnapshot(t, wager.Processed)}, http.StatusOK, false, map[string]any{
			"status": "PROCESSED", "balance": map[string]any{"amount": "975.00", "currency": "BRL"}, "idempotentReplay": false,
		}},
		{"replay", app.WagerResult{Transaction: betSnapshot(t, wager.Processed), IdempotentReplay: true}, http.StatusOK, false, map[string]any{
			"status": "PROCESSED", "idempotentReplay": true,
		}},
		{"pending reference", app.WagerResult{Transaction: betSnapshot(t, wager.PendingReference)}, http.StatusAccepted, true, map[string]any{
			"status": "PENDING_REFERENCE", "referenceDeadlineAt": "2026-09-26T12:15:00Z", "balance": nil,
		}},
		{"rejection", app.WagerResult{Transaction: betSnapshot(t, wager.Rejected)}, http.StatusUnprocessableEntity, false, map[string]any{
			"status": "REJECTED", "failureCode": "INSUFFICIENT_FUNDS", "balance": map[string]any{"amount": "10.00", "currency": "BRL"},
		}},
		{"recorded failure", app.WagerResult{Transaction: betSnapshot(t, wager.Failed), IdempotentReplay: true}, http.StatusInternalServerError, false, map[string]any{
			"status": "FAILED", "failureCode": "PROCESSING_FAILED", "idempotentReplay": true,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got app.SubmitWager
			wagers := &fakeWagers{submit: func(cmd app.SubmitWager) (app.WagerResult, error) {
				got = cmd
				return tt.result, nil
			}}
			r := serve(t, &fakeWallets{}, wagers, submit(betBody, "provider-a", map[string]string{"X-Correlation-Id": "corr-1"}))
			if r.status != tt.status || r.header.Get("Content-Type") != "application/json" {
				t.Fatalf("status %d (%s); want %d", r.status, r.raw, tt.status)
			}
			if r.body["transactionId"] != txID.String() || r.body["externalTransactionId"] != "transaction-123" {
				t.Fatalf("body = %s", r.raw)
			}
			for k, v := range tt.fields {
				if got := r.body[k]; !reflect.DeepEqual(got, v) {
					t.Fatalf("%s = %v; want %v (%s)", k, got, v, r.raw)
				}
			}
			if loc := r.header.Get("Location"); (loc == "/wagering/transactions/"+txID.String()) != tt.location {
				t.Fatalf("Location = %q", loc)
			}
			if got.IdempotencyKey != "provider-a:transaction-123" || got.CorrelationID != "corr-1" || got.Channel != app.ChannelHTTP ||
				got.ProviderID != "provider-a" || r.header.Get(correlationHeader) != "corr-1" {
				t.Fatalf("command = %+v, echoed correlation %q", got, r.header.Get(correlationHeader))
			}
		})
	}
}

func TestSubmitRejectsBeforeTheUseCase(t *testing.T) {
	tests := []struct {
		name   string
		call   call
		status int
		code   string
		issues []issue
	}{
		{"missing idempotency key", call{method: http.MethodPost, path: "/wagering/transactions", token: "provider-a", body: betBody},
			http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", []issue{{"Idempotency-Key", "REQUIRED"}}},
		{"invalid field", submit(strings.Replace(betBody, `"25.00"`, `"25"`, 1), "provider-a", nil),
			http.StatusBadRequest, "INVALID_REQUEST", []issue{{"money.amount", "INVALID_AMOUNT"}}},
		{"every invalid field", submit(`{"kind": "OPENING", "money": {"amount": "1.00", "currency": "BRL"}}`, "provider-a", nil),
			http.StatusBadRequest, "INVALID_REQUEST", []issue{
				{"providerId", "REQUIRED"}, {"externalTransactionId", "REQUIRED"}, {"playerId", "REQUIRED"},
				{"walletId", "REQUIRED"}, {"roundId", "REQUIRED"}, {"gameId", "REQUIRED"},
			}},
		{"unknown field", submit(strings.Replace(betBody, `"kind"`, `"bonus": true, "kind"`, 1), "provider-a", nil),
			http.StatusBadRequest, "INVALID_REQUEST", []issue{{"bonus", "UNKNOWN_FIELD"}}},
		{"amount as a JSON number", submit(strings.Replace(betBody, `"25.00"`, `25.00`, 1), "provider-a", nil),
			http.StatusBadRequest, "INVALID_REQUEST", []issue{{"money.amount", "INVALID_VALUE"}}},
		{"trailing data", submit(betBody+`{}`, "provider-a", nil), http.StatusBadRequest, "INVALID_REQUEST", nil},
		{"not JSON", submit(`{"providerId":`, "provider-a", nil), http.StatusBadRequest, "INVALID_REQUEST", nil},
		{"empty body", call{method: http.MethodPost, path: "/wagering/transactions", token: "provider-a",
			headers: map[string]string{"Idempotency-Key": "k", "Content-Type": "application/json"}}, http.StatusBadRequest, "INVALID_REQUEST", nil},
		{"wrong content type", call{method: http.MethodPost, path: "/wagering/transactions", token: "provider-a", body: betBody,
			contentType: "text/plain", headers: map[string]string{"Idempotency-Key": "k"}}, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", nil},
		{"body over 64 KiB", submit(strings.Replace(betBody, `"round-987"`, `"`+strings.Repeat("r", 70000)+`"`, 1), "provider-a", nil),
			http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", nil},
		{"invalid correlation id", submit(betBody, "provider-a", map[string]string{"X-Correlation-Id": "has space"}),
			http.StatusBadRequest, "INVALID_REQUEST", []issue{{"X-Correlation-Id", "INVALID_VALUE"}}},
		{"another provider's id", submit(betBody, "provider-b", nil), http.StatusForbidden, "FORBIDDEN", nil},
		{"operator token", submit(betBody, "operator", nil), http.StatusForbidden, "FORBIDDEN", nil},
		{"token without a role", submit(betBody, "no-role", nil), http.StatusForbidden, "FORBIDDEN", nil},
		{"no token", submit(betBody, "", nil), http.StatusUnauthorized, "UNAUTHENTICATED", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wagers := &fakeWagers{}
			serve(t, &fakeWallets{}, wagers, tt.call).requireProblem(t, tt.status, tt.code, tt.issues...)
			if wagers.calls != 0 {
				t.Fatalf("the use case ran %d times", wagers.calls)
			}
		})
	}
}

func TestSubmitErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"key reused", fmt.Errorf("%w: key belongs to another payload", app.ErrIdempotencyKeyReused), http.StatusConflict, "IDEMPOTENCY_KEY_REUSED"},
		{"external id under another key", app.ErrExternalIDConflict, http.StatusConflict, "EXTERNAL_TRANSACTION_ID_CONFLICT"},
		{"database down", fmt.Errorf("%w: connection refused", app.ErrTransient), http.StatusServiceUnavailable, "TEMPORARILY_UNAVAILABLE"},
		{"lost race retried out", app.ErrConcurrentUpdate, http.StatusServiceUnavailable, "TEMPORARILY_UNAVAILABLE"},
		{"request deadline", context.DeadlineExceeded, http.StatusServiceUnavailable, "TEMPORARILY_UNAVAILABLE"},
		{"corrupt row", fmt.Errorf("%w: corrupt stored state: %w", app.ErrPermanent, domain.NewFieldError("walletId", domain.ErrRequired)),
			http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wagers := &fakeWagers{submit: func(app.SubmitWager) (app.WagerResult, error) { return app.WagerResult{}, tt.err }}
			r := serve(t, &fakeWallets{}, wagers, submit(betBody, "provider-a", nil))
			r.requireProblem(t, tt.status, tt.code)
			if strings.Contains(r.raw, "connection refused") || strings.Contains(r.raw, "corrupt") {
				t.Fatalf("internal detail leaked: %s", r.raw)
			}
		})
	}
}

func TestCorrelationIDIsGeneratedWhenAbsent(t *testing.T) {
	var got app.SubmitWager
	wagers := &fakeWagers{submit: func(cmd app.SubmitWager) (app.WagerResult, error) {
		got = cmd
		return app.WagerResult{Transaction: betSnapshot(t, wager.Processed)}, nil
	}}
	r := serve(t, &fakeWallets{}, wagers, submit(betBody, "provider-a", nil))
	id, err := uuid.Parse(r.header.Get(correlationHeader))
	if r.status != http.StatusOK || err != nil || id.Version() != 7 || got.CorrelationID != id.String() {
		t.Fatalf("correlation header %q (%v), command %q; want one UUIDv7 in both", r.header.Get(correlationHeader), err, got.CorrelationID)
	}
}

func TestWalletRoutes(t *testing.T) {
	opened := walletSnapshot(t)
	existing := uuid.MustParse("0192f2a0-51c3-7b8e-9f4d-2c6e8a0b1d3f")
	entry := ledger.Snapshot{ID: uuid.New(), WalletID: walletID, TransactionID: txID, Direction: ledger.Credit, Amount: brl(t, "1000.00"),
		BalanceBefore: brl(t, "0.00"), BalanceAfter: brl(t, "1000.00"), WalletVersion: 1, CreatedAt: t0}
	var gotCursor string
	var gotLimit int
	wallets := func() *fakeWallets {
		return &fakeWallets{
			open: func(cmd app.OpenWallet) (wallet.Snapshot, error) {
				if cmd.PlayerID == existing {
					return wallet.Snapshot{}, &app.WalletExistsError{WalletID: existing}
				}
				return opened, nil
			},
			get: func(id uuid.UUID) (wallet.Snapshot, error) {
				if id != walletID {
					return wallet.Snapshot{}, domain.ErrWalletNotFound
				}
				return opened, nil
			},
			ledger: func(_ uuid.UUID, cursor string, limit int) (app.LedgerPage, error) {
				gotCursor, gotLimit = cursor, limit
				if cursor == "bad" {
					return app.LedgerPage{}, domain.NewFieldError("cursor", domain.ErrInvalidValue)
				}
				return app.LedgerPage{Entries: []ledger.Snapshot{entry}, NextCursor: "MQ"}, nil
			},
			reconcile: func(uuid.UUID) (app.Reconciliation, error) {
				return app.Reconciliation{WalletID: walletID, StoredBalance: brl(t, "85.00"), CalculatedBalance: brl(t, "80.00"),
					Difference: brl(t, "5.00"), CheckedEntries: 3, ContinuousVersions: true}, nil
			},
		}
	}
	openBody := func(player uuid.UUID) string {
		return `{"playerId": "` + player.String() + `", "initialBalance": {"amount": "1000.00", "currency": "BRL"}}`
	}
	path := "/wallets/" + walletID.String()

	r := serve(t, wallets(), &fakeWagers{}, call{method: http.MethodPost, path: "/wallets", token: "operator", body: openBody(playerID)})
	if r.status != http.StatusCreated || r.header.Get("Location") != path || r.body["id"] != walletID.String() || r.body["version"] != float64(1) ||
		!reflect.DeepEqual(r.body["balance"], map[string]any{"amount": "1000.00", "currency": "BRL"}) {
		t.Fatalf("open = %d %s (Location %q)", r.status, r.raw, r.header.Get("Location"))
	}
	r = serve(t, wallets(), &fakeWagers{}, call{method: http.MethodPost, path: "/wallets", token: "operator", body: openBody(existing)})
	r.requireProblem(t, http.StatusConflict, "WALLET_ALREADY_EXISTS")
	if r.header.Get("Location") != "/wallets/"+existing.String() {
		t.Fatalf("conflict Location = %q", r.header.Get("Location"))
	}
	serve(t, wallets(), &fakeWagers{}, call{method: http.MethodPost, path: "/wallets", token: "operator",
		body: `{"playerId": "p-1", "initialBalance": {"amount": "-1.00", "currency": "BRL"}}`}).
		requireProblem(t, http.StatusBadRequest, "INVALID_REQUEST", issue{"playerId", "INVALID_VALUE"}, issue{"initialBalance.amount", "INVALID_AMOUNT"})

	r = serve(t, wallets(), &fakeWagers{}, call{method: http.MethodGet, path: path, token: "operator"})
	if r.status != http.StatusOK || r.body["playerId"] != playerID.String() || r.body["createdAt"] != "2026-09-26T12:00:00Z" {
		t.Fatalf("get = %d %s", r.status, r.raw)
	}
	serve(t, wallets(), &fakeWagers{}, call{method: http.MethodGet, path: "/wallets/" + existing.String(), token: "operator"}).
		requireProblem(t, http.StatusNotFound, "WALLET_NOT_FOUND")
	serve(t, wallets(), &fakeWagers{}, call{method: http.MethodGet, path: "/wallets/not-a-uuid", token: "operator"}).
		requireProblem(t, http.StatusBadRequest, "INVALID_REQUEST", issue{"walletId", "INVALID_VALUE"})

	r = serve(t, wallets(), &fakeWagers{}, call{method: http.MethodGet, path: path + "/ledger?limit=2&cursor=MA", token: "operator"})
	entries, _ := r.body["entries"].([]any)
	if r.status != http.StatusOK || gotCursor != "MA" || gotLimit != 2 || len(entries) != 1 || r.body["nextCursor"] != "MQ" ||
		entries[0].(map[string]any)["direction"] != "CREDIT" || entries[0].(map[string]any)["walletVersion"] != float64(1) {
		t.Fatalf("ledger = %d %s (cursor %q, limit %d)", r.status, r.raw, gotCursor, gotLimit)
	}
	serve(t, wallets(), &fakeWagers{}, call{method: http.MethodGet, path: path + "/ledger", token: "operator"})
	if gotLimit != 0 {
		t.Fatalf("absent limit reached the use case as %d; want 0 for the default", gotLimit)
	}
	for _, limit := range []string{"0", "-1", "ten", "1.5"} {
		serve(t, wallets(), &fakeWagers{}, call{method: http.MethodGet, path: path + "/ledger?limit=" + limit, token: "operator"}).
			requireProblem(t, http.StatusBadRequest, "INVALID_REQUEST", issue{"limit", "INVALID_VALUE"})
	}
	serve(t, wallets(), &fakeWagers{}, call{method: http.MethodGet, path: path + "/ledger?cursor=bad", token: "operator"}).
		requireProblem(t, http.StatusBadRequest, "INVALID_REQUEST", issue{"cursor", "INVALID_VALUE"})

	r = serve(t, wallets(), &fakeWagers{}, call{method: http.MethodPost, path: path + "/reconciliation", token: "operator"})
	want := map[string]any{
		"walletId":           walletID.String(),
		"storedBalance":      map[string]any{"amount": "85.00", "currency": "BRL"},
		"calculatedBalance":  map[string]any{"amount": "80.00", "currency": "BRL"},
		"difference":         map[string]any{"amount": "5.00", "currency": "BRL"},
		"consistent":         false,
		"continuousVersions": true,
		"checkedEntries":     float64(3),
	}
	if r.status != http.StatusOK || !reflect.DeepEqual(r.body, want) {
		t.Fatalf("reconciliation = %d %s", r.status, r.raw)
	}

	for _, token := range []string{"provider-a", "no-role"} {
		for _, c := range []call{
			{method: http.MethodPost, path: "/wallets", token: token, body: openBody(playerID)},
			{method: http.MethodGet, path: path, token: token},
			{method: http.MethodGet, path: path + "/ledger", token: token},
			{method: http.MethodPost, path: path + "/reconciliation", token: token},
		} {
			w := wallets()
			serve(t, w, &fakeWagers{}, c).requireProblem(t, http.StatusForbidden, "FORBIDDEN")
			if w.calls != 0 {
				t.Fatalf("%s %s as %s reached the use case", c.method, c.path, token)
			}
		}
	}
}

func TestTransactionReads(t *testing.T) {
	ofA := betSnapshot(t, wager.Processed)
	wagers := func() *fakeWagers {
		return &fakeWagers{
			get: func(id uuid.UUID) (wager.Snapshot, error) {
				if id != txID {
					return wager.Snapshot{}, domain.ErrTransactionNotFound
				}
				return ofA, nil
			},
			external: func(providerID, externalID string) (wager.Snapshot, error) {
				if providerID != "provider-a" || externalID != "transaction-123" {
					return wager.Snapshot{}, domain.ErrTransactionNotFound
				}
				return ofA, nil
			},
		}
	}
	byID := "/wagering/transactions/" + txID.String()
	missing := "/wagering/transactions/" + uuid.NewString()
	byExternal := "/providers/provider-a/wagering/transactions/transaction-123"

	for _, token := range []string{"provider-a", "operator"} {
		for _, path := range []string{byID, byExternal} {
			r := serve(t, &fakeWallets{}, wagers(), call{method: http.MethodGet, path: path, token: token})
			if r.status != http.StatusOK || r.body["transactionId"] != txID.String() || r.body["origin"] != "EXTERNAL" ||
				r.body["correlationId"] != "corr-1" || r.body["idempotencyKey"] != "provider-a:transaction-123" {
				t.Fatalf("GET %s as %s = %d %s", path, token, r.status, r.raw)
			}
		}
	}

	hidden := serve(t, &fakeWallets{}, wagers(), call{method: http.MethodGet, path: byID, token: "provider-b"})
	absent := serve(t, &fakeWallets{}, wagers(), call{method: http.MethodGet, path: missing, token: "provider-b"})
	hidden.requireProblem(t, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
	if hidden.raw != absent.raw {
		t.Fatalf("another provider's transaction answers %s; a missing one answers %s", hidden.raw, absent.raw)
	}

	for _, c := range []call{
		{method: http.MethodGet, path: byExternal, token: "provider-b"},
		{method: http.MethodGet, path: byID, token: "no-role"},
		{method: http.MethodGet, path: byExternal, token: "no-role"},
	} {
		w := wagers()
		serve(t, &fakeWallets{}, w, c).requireProblem(t, http.StatusForbidden, "FORBIDDEN")
		if w.calls != 0 {
			t.Fatalf("GET %s as %s reached the use case", c.path, c.token)
		}
	}
	serve(t, &fakeWallets{}, wagers(), call{method: http.MethodGet, path: "/providers/provider-a/wagering/transactions/nope", token: "provider-a"}).
		requireProblem(t, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
	serve(t, &fakeWallets{}, wagers(), call{method: http.MethodGet, path: "/wagering/transactions/42", token: "operator"}).
		requireProblem(t, http.StatusBadRequest, "INVALID_REQUEST", issue{"transactionId", "INVALID_VALUE"})
}

func TestUnknownPaths(t *testing.T) {
	serve(t, &fakeWallets{}, &fakeWagers{}, call{method: http.MethodGet, path: "/admin", token: "operator"}).
		requireProblem(t, http.StatusNotFound, "NOT_FOUND")
	serve(t, &fakeWallets{}, &fakeWagers{}, call{method: http.MethodGet, path: "/admin"}).
		requireProblem(t, http.StatusUnauthorized, "UNAUTHENTICATED")
	if r := serve(t, &fakeWallets{}, &fakeWagers{}, call{method: http.MethodGet, path: "/health/live"}); r.status != http.StatusOK {
		t.Fatalf("health without a token = %d", r.status)
	}
}
