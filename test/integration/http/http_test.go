//go:build integration

package http_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/httpapi"
	"github.com/mhetem/backend-challenge-go-jungle/internal/bootstrap"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
)

type stack struct {
	t      *testing.T
	db     *dbtest.Database
	base   string
	tokens map[string]string
	http   *http.Client
}

func load(t *testing.T, overrides map[string]string) config.Config {
	t.Helper()
	cfg, err := config.Parse(func(key string) (string, bool) {
		if v, ok := overrides[key]; ok {
			return v, true
		}
		return os.LookupEnv(key)
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func start(t *testing.T) *stack {
	t.Helper()
	db := dbtest.New(t)
	cfg := load(t, map[string]string{
		"INSTANCE_ID":  "http-test",
		"COMPONENTS":   "http,resolver",
		"LOG_LEVEL":    "warn",
		"HTTP_ADDR":    "127.0.0.1:0",
		"ADMIN_ADDR":   "127.0.0.1:0",
		"DATABASE_URL": db.AppURL,
	})
	var public *httpapi.Server
	service := fxtest.New(t, bootstrap.Options(cfg), fx.Populate(&public))
	service.RequireStart()
	t.Cleanup(service.RequireStop)
	s := &stack{
		t:      t,
		db:     db,
		base:   "http://" + public.Addr(),
		tokens: map[string]string{},
		http:   &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 15 * time.Second},
	}
	for _, client := range []string{"provider-a", "provider-b", "wallet-backoffice", "no-role-client"} {
		s.tokens[client] = s.token(cfg.OIDC.Issuer, client)
	}
	return s
}

func (s *stack) token(issuer, clientID string) string {
	s.t.Helper()
	secret := dbtest.Env(s.t, strings.ToUpper(strings.ReplaceAll(clientID, "-", "_"))+"_SECRET")
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	resp, err := s.http.PostForm(issuer+"/protocol/openid-connect/token", form)
	if err != nil {
		s.t.Fatalf("token for %s: %v", clientID, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		s.t.Fatalf("token for %s: %s, %v", clientID, resp.Status, err)
	}
	return body.AccessToken
}

type response struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r response) money(field string) string {
	m, _ := r.body[field].(map[string]any)
	amount, _ := m["amount"].(string)
	currency, _ := m["currency"].(string)
	return amount + " " + currency
}

func (s *stack) do(method, path, client string, headers map[string]string, body any) response {
	s.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(s.t.Context(), method, s.base+path, reader)
	if err != nil {
		s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client != "" {
		req.Header.Set("Authorization", "Bearer "+s.tokens[client])
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	r := response{status: resp.StatusCode, header: resp.Header, raw: buf.String()}
	_ = json.Unmarshal(buf.Bytes(), &r.body)
	return r
}

func (s *stack) expect(r response, status int, code string) response {
	s.t.Helper()
	if r.status != status || code != "" && r.body["code"] != code {
		s.t.Fatalf("got %d %s; want %d %s", r.status, r.raw, status, code)
	}
	return r
}

func (s *stack) openWallet(amount string) string {
	s.t.Helper()
	r := s.expect(s.do(http.MethodPost, "/wallets", "wallet-backoffice", nil, map[string]any{
		"playerId":       uuid.NewString(),
		"initialBalance": map[string]string{"amount": amount, "currency": "BRL"},
	}), http.StatusCreated, "")
	return r.body["id"].(string)
}

func (s *stack) wallet(id string) response {
	s.t.Helper()
	return s.expect(s.do(http.MethodGet, "/wallets/"+id, "wallet-backoffice", nil, nil), http.StatusOK, "")
}

type wagerBody map[string]any

func bet(walletID, playerID, provider, extID, kind, amount, ref string) wagerBody {
	b := wagerBody{
		"providerId":            provider,
		"externalTransactionId": extID,
		"playerId":              playerID,
		"walletId":              walletID,
		"roundId":               "round-1",
		"gameId":                "game-1",
		"kind":                  kind,
		"money":                 map[string]string{"amount": amount, "currency": "BRL"},
	}
	if ref != "" {
		b["referenceExternalTransactionId"] = ref
	}
	return b
}

func (s *stack) submit(client string, body wagerBody, headers map[string]string) response {
	s.t.Helper()
	h := map[string]string{"Idempotency-Key": body["providerId"].(string) + ":" + body["externalTransactionId"].(string)}
	for k, v := range headers {
		h[k] = v
	}
	return s.do(http.MethodPost, "/wagering/transactions", client, h, body)
}

func TestWagerFlow(t *testing.T) {
	t.Parallel()
	s := start(t)
	walletID := s.openWallet("100.00")
	player := s.wallet(walletID).body["playerId"].(string)
	wager := func(extID, kind, amount, ref string) wagerBody {
		return bet(walletID, player, "provider-a", extID, kind, amount, ref)
	}

	first := s.expect(s.submit("provider-a", wager("bet-1", "BET", "25.00", ""), map[string]string{"X-Correlation-Id": "flow-1"}), http.StatusOK, "")
	if first.body["status"] != "PROCESSED" || first.money("balance") != "75.00 BRL" || first.body["idempotentReplay"] != false ||
		first.header.Get("X-Correlation-Id") != "flow-1" {
		t.Fatalf("bet = %s (correlation %q)", first.raw, first.header.Get("X-Correlation-Id"))
	}
	replay := s.expect(s.submit("provider-a", wager("bet-1", "BET", "25.00", ""), nil), http.StatusOK, "")
	if replay.body["idempotentReplay"] != true || replay.body["transactionId"] != first.body["transactionId"] || replay.money("balance") != "75.00 BRL" {
		t.Fatalf("replay = %s", replay.raw)
	}
	s.expect(s.submit("provider-a", wager("bet-1", "BET", "30.00", ""), nil), http.StatusConflict, "IDEMPOTENCY_KEY_REUSED")
	s.expect(s.do(http.MethodPost, "/wagering/transactions", "provider-a", map[string]string{"Idempotency-Key": "provider-a:bet-1:again"},
		wager("bet-1", "BET", "25.00", "")), http.StatusConflict, "EXTERNAL_TRANSACTION_ID_CONFLICT")
	s.expect(s.submit("provider-a", wager("win-1", "WIN", "10.00", ""), nil), http.StatusOK, "")

	pending := s.expect(s.submit("provider-a", wager("rollback-1", "ROLLBACK", "25.00", "bet-9"), nil), http.StatusAccepted, "")
	location := pending.header.Get("Location")
	if pending.body["status"] != "PENDING_REFERENCE" || location != "/wagering/transactions/"+pending.body["transactionId"].(string) ||
		pending.body["referenceDeadlineAt"] == nil {
		t.Fatalf("pending rollback = %s (Location %q)", pending.raw, location)
	}
	tracked := s.expect(s.do(http.MethodGet, location, "provider-a", nil, nil), http.StatusOK, "")
	if tracked.body["status"] != "PENDING_REFERENCE" || tracked.body["attempts"] != float64(1) || tracked.body["nextAttemptAt"] == nil {
		t.Fatalf("tracked pending rollback = %s", tracked.raw)
	}

	rejected := s.expect(s.submit("provider-a", wager("bet-2", "BET", "500.00", ""), nil), http.StatusUnprocessableEntity, "")
	if rejected.body["status"] != "REJECTED" || rejected.body["failureCode"] != "INSUFFICIENT_FUNDS" || rejected.money("balance") != "85.00 BRL" {
		t.Fatalf("rejected bet = %s", rejected.raw)
	}

	byExternal := s.expect(s.do(http.MethodGet, "/providers/provider-a/wagering/transactions/bet-1", "provider-a", nil, nil), http.StatusOK, "")
	if byExternal.body["transactionId"] != first.body["transactionId"] || byExternal.body["correlationId"] != "flow-1" {
		t.Fatalf("bet-1 by external id = %s", byExternal.raw)
	}
	if w := s.wallet(walletID); w.money("balance") != "85.00 BRL" || w.body["version"] != float64(3) {
		t.Fatalf("wallet = %s", w.raw)
	}
	ledger := s.expect(s.do(http.MethodGet, "/wallets/"+walletID+"/ledger", "wallet-backoffice", nil, nil), http.StatusOK, "")
	if entries, _ := ledger.body["entries"].([]any); len(entries) != 3 || ledger.body["nextCursor"] != nil {
		t.Fatalf("ledger = %s", ledger.raw)
	}
	recon := s.expect(s.do(http.MethodPost, "/wallets/"+walletID+"/reconciliation", "wallet-backoffice", nil, nil), http.StatusOK, "")
	if recon.body["consistent"] != true || recon.body["checkedEntries"] != float64(3) || recon.money("difference") != "0.00 BRL" {
		t.Fatalf("reconciliation = %s", recon.raw)
	}

	again := s.expect(s.do(http.MethodPost, "/wallets", "wallet-backoffice", nil, map[string]any{
		"playerId":       player,
		"initialBalance": map[string]string{"amount": "5.00", "currency": "BRL"},
	}), http.StatusConflict, "WALLET_ALREADY_EXISTS")
	if again.header.Get("Location") != "/wallets/"+walletID {
		t.Fatalf("existing wallet Location = %q; want /wallets/%s", again.header.Get("Location"), walletID)
	}
}

func TestLedgerPaginationWhileAppending(t *testing.T) {
	t.Parallel()
	s := start(t)
	walletID := s.openWallet("1000.00")
	player := s.wallet(walletID).body["playerId"].(string)
	placed := 0
	place := func(n int) {
		for range n {
			placed++
			extID := fmt.Sprintf("bet-%d", placed)
			s.expect(s.submit("provider-a", bet(walletID, player, "provider-a", extID, "BET", "1.00", ""), nil), http.StatusOK, "")
		}
	}
	place(5)

	var seen []float64
	cursor := ""
	for pages := 0; ; pages++ {
		path := "/wallets/" + walletID + "/ledger?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page := s.expect(s.do(http.MethodGet, path, "wallet-backoffice", nil, nil), http.StatusOK, "")
		for _, e := range page.body["entries"].([]any) {
			seen = append(seen, e.(map[string]any)["walletVersion"].(float64))
		}
		next, _ := page.body["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
		if pages < 3 {
			place(2)
		}
	}

	want := make([]float64, 0, placed+1)
	for v := 1; v <= placed+1; v++ {
		want = append(want, float64(v))
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("paged through versions %v; want every version 1..%d once, in order", seen, placed+1)
	}
}

type footprint struct {
	transactions, entries, events int
	balance                       string
}

func (s *stack) footprint(walletID string) footprint {
	s.t.Helper()
	var f footprint
	err := s.db.App.QueryRow(s.t.Context(), `SELECT
		(SELECT count(*) FROM wager_transactions),
		(SELECT count(*) FROM ledger_entries),
		(SELECT count(*) FROM outbox_events)`).Scan(&f.transactions, &f.entries, &f.events)
	if err != nil {
		s.t.Fatal(err)
	}
	f.balance = s.wallet(walletID).money("balance")
	return f
}

func TestProvidersAreIsolated(t *testing.T) {
	t.Parallel()
	s := start(t)
	walletID := s.openWallet("100.00")
	player := s.wallet(walletID).body["playerId"].(string)
	ofA := bet(walletID, player, "provider-a", "a-bet-1", "BET", "20.00", "")
	placed := s.expect(s.submit("provider-a", ofA, nil), http.StatusOK, "")
	aID := placed.body["transactionId"].(string)
	before := s.footprint(walletID)

	leaks := func(r response) bool {
		return strings.Contains(r.raw, aID) || strings.Contains(r.raw, "a-bet-1") || strings.Contains(r.raw, "80.00")
	}
	calls := []struct {
		name   string
		do     func() response
		status int
		code   string
	}{
		{"B submits as A", func() response {
			return s.submit("provider-b", bet(walletID, player, "provider-a", "a-bet-2", "BET", "20.00", ""), nil)
		}, http.StatusForbidden, "FORBIDDEN"},
		{"B replays A's operation", func() response {
			return s.submit("provider-b", ofA, nil)
		}, http.StatusForbidden, "FORBIDDEN"},
		{"B reads A's transaction by id", func() response {
			return s.do(http.MethodGet, "/wagering/transactions/"+aID, "provider-b", nil, nil)
		}, http.StatusNotFound, "TRANSACTION_NOT_FOUND"},
		{"B reads A's transaction by external id", func() response {
			return s.do(http.MethodGet, "/providers/provider-a/wagering/transactions/a-bet-1", "provider-b", nil, nil)
		}, http.StatusForbidden, "FORBIDDEN"},
		{"a client without a role submits", func() response {
			return s.submit("no-role-client", bet(walletID, player, "provider-a", "a-bet-3", "BET", "20.00", ""), nil)
		}, http.StatusForbidden, "FORBIDDEN"},
		{"A opens a wallet", func() response {
			return s.do(http.MethodPost, "/wallets", "provider-a", nil, map[string]any{
				"playerId": uuid.NewString(), "initialBalance": map[string]string{"amount": "1.00", "currency": "BRL"},
			})
		}, http.StatusForbidden, "FORBIDDEN"},
		{"A reads the wallet", func() response {
			return s.do(http.MethodGet, "/wallets/"+walletID, "provider-a", nil, nil)
		}, http.StatusForbidden, "FORBIDDEN"},
		{"A reads the ledger", func() response {
			return s.do(http.MethodGet, "/wallets/"+walletID+"/ledger", "provider-a", nil, nil)
		}, http.StatusForbidden, "FORBIDDEN"},
		{"A reconciles", func() response {
			return s.do(http.MethodPost, "/wallets/"+walletID+"/reconciliation", "provider-a", nil, nil)
		}, http.StatusForbidden, "FORBIDDEN"},
		{"no token", func() response {
			return s.submit("", bet(walletID, player, "provider-a", "a-bet-4", "BET", "20.00", ""), nil)
		}, http.StatusUnauthorized, "UNAUTHENTICATED"},
	}
	for _, c := range calls {
		r := c.do()
		if r.status != c.status || r.body["code"] != c.code || leaks(r) {
			t.Fatalf("%s = %d %s; want %d %s without A's data", c.name, r.status, r.raw, c.status, c.code)
		}
	}

	if after := s.footprint(walletID); after != before {
		t.Fatalf("unauthorized calls changed the system: before %+v, after %+v", before, after)
	}
	own := s.expect(s.do(http.MethodGet, "/wagering/transactions/"+aID, "provider-a", nil, nil), http.StatusOK, "")
	if own.body["externalTransactionId"] != "a-bet-1" {
		t.Fatalf("A reading its own transaction = %s", own.raw)
	}
}
