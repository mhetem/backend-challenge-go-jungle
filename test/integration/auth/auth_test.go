//go:build integration

package auth_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"go.uber.org/goleak"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/httpapi"
	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/oidc"
	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
	"github.com/mhetem/backend-challenge-go-jungle/internal/bootstrap"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
)

var client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}

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

func verifier(t *testing.T) *oidc.Verifier {
	t.Helper()
	cfg := load(t, nil)
	v := oidc.New(cfg.OIDC.Issuer, cfg.OIDC.JWKSURL, cfg.OIDC.Audience)
	t.Cleanup(v.Stop)
	return v
}

func token(t *testing.T, clientID string) string {
	t.Helper()
	secret := dbtest.Env(t, strings.ToUpper(strings.ReplaceAll(clientID, "-", "_"))+"_SECRET")
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	resp, err := client.PostForm(load(t, nil).OIDC.Issuer+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("token for %s: %v", clientID, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		t.Fatalf("token for %s: %s, %v", clientID, resp.Status, err)
	}
	return body.AccessToken
}

func claims(t *testing.T, raw string) map[string]any {
	t.Helper()
	parts := strings.Split(raw, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func authenticate(t *testing.T, v *oidc.Verifier, clientID string) auth.Principal {
	t.Helper()
	p, err := v.Authenticate(t.Context(), token(t, clientID))
	if err != nil {
		t.Fatalf("%s: %v", clientID, err)
	}
	return p
}

func requireErr(t *testing.T, name string, got, want error) {
	t.Helper()
	if want == nil && got != nil || want != nil && !errors.Is(got, want) {
		t.Fatalf("%s = %v; want %v", name, got, want)
	}
}

func TestServiceAccountsBecomePrincipals(t *testing.T) {
	t.Parallel()
	v := verifier(t)
	a := authenticate(t, v, "provider-a")
	b := authenticate(t, v, "provider-b")
	operator := authenticate(t, v, "wallet-backoffice")
	noRole := authenticate(t, v, "no-role-client")

	for _, tt := range []struct {
		principal auth.Principal
		client    string
		provider  string
		role      auth.Role
	}{
		{a, "provider-a", "provider-a", auth.WagerProvider},
		{b, "provider-b", "provider-b", auth.WagerProvider},
		{operator, "wallet-backoffice", "", auth.WalletOperator},
	} {
		p := tt.principal
		if p.ClientID != tt.client || p.ProviderID != tt.provider || !p.Has(tt.role) || p.Subject == "" {
			t.Fatalf("%s principal = %+v; want provider %q with role %s", tt.client, p, tt.provider, tt.role)
		}
	}
	if a.Has(auth.WalletOperator) || operator.Has(auth.WagerProvider) || noRole.Has(auth.WagerProvider) || noRole.Has(auth.WalletOperator) {
		t.Fatalf("roles leaked: a=%v operator=%v no-role=%v", a.Roles, operator.Roles, noRole.Roles)
	}

	ofA := wager.Snapshot{ProviderID: "provider-a"}
	requireErr(t, "A submits as A", auth.SubmitWager(a, "provider-a"), nil)
	requireErr(t, "B submits as A", auth.SubmitWager(b, "provider-a"), auth.ErrForbidden)
	requireErr(t, "operator submits", auth.SubmitWager(operator, "provider-a"), auth.ErrForbidden)
	requireErr(t, "no role submits", auth.SubmitWager(noRole, "provider-a"), auth.ErrForbidden)
	requireErr(t, "B reads A's external id", auth.ReadProviderTransaction(b, "provider-a"), auth.ErrForbidden)
	requireErr(t, "B reads A's transaction by id", auth.ReadTransaction(b, ofA), domain.ErrTransactionNotFound)
	requireErr(t, "operator reads A's transaction", auth.ReadTransaction(operator, ofA), nil)
	requireErr(t, "provider opens wallets", auth.ManageWallets(a), auth.ErrForbidden)
	requireErr(t, "no role opens wallets", auth.ManageWallets(noRole), auth.ErrForbidden)
	requireErr(t, "operator opens wallets", auth.ManageWallets(operator), nil)
}

func TestRejectedTokens(t *testing.T) {
	t.Parallel()
	v := verifier(t)
	valid := token(t, "provider-b")
	parts := strings.Split(valid, ".")
	forged := claims(t, valid)
	forged["provider_id"] = "provider-a"
	payload, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	middle := len(parts[2]) / 2
	flipped := byte('A')
	if parts[2][middle] == 'A' {
		flipped = 'B'
	}

	tests := map[string]string{
		"wrong audience":   token(t, "no-audience-client"),
		"forged provider":  parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2],
		"broken signature": parts[0] + "." + parts[1] + "." + parts[2][:middle] + string(flipped) + parts[2][middle+1:],
		"malformed":        "not.a.token",
	}
	for name, raw := range tests {
		if p, err := v.Authenticate(t.Context(), raw); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("%s: Authenticate = %+v, %v; want %v", name, p, err, auth.ErrUnauthenticated)
		}
	}
	if _, err := v.Authenticate(t.Context(), valid); err != nil {
		t.Fatalf("the untouched token stopped verifying: %v", err)
	}
}

func TestExpiredToken(t *testing.T) {
	t.Parallel()
	v := verifier(t)
	raw := token(t, "provider-a-shortlived")
	c := claims(t, raw)
	exp, iat := int64(c["exp"].(float64)), int64(c["iat"].(float64))
	if exp-iat > 10 {
		t.Fatalf("short-lived token lasts %ds; the realm should give it seconds", exp-iat)
	}
	if _, err := v.Authenticate(t.Context(), raw); err != nil {
		t.Fatalf("fresh short-lived token: %v", err)
	}
	time.Sleep(time.Until(time.Unix(exp+1, 0)))
	if _, err := v.Authenticate(t.Context(), raw); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expired token: err = %v; want %v", err, auth.ErrUnauthenticated)
	}
}

type response struct {
	status    int
	challenge string
	code      string
}

func get(t *testing.T, url, bearer string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var problem struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &problem)
	return response{status: resp.StatusCode, challenge: resp.Header.Get("WWW-Authenticate"), code: problem.Code}
}

func TestServerRequiresATokenOutsideHealth(t *testing.T) {
	db := dbtest.New(t)
	cfg := load(t, map[string]string{
		"INSTANCE_ID":  "auth-test",
		"LOG_LEVEL":    "warn",
		"HTTP_ADDR":    "127.0.0.1:0",
		"ADMIN_ADDR":   "127.0.0.1:0",
		"DATABASE_URL": db.AppURL,
	})
	valid, forged := token(t, "provider-a"), "eyJhbGciOiJSUzI1NiJ9.e30.c2lnbmF0dXJl"
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var public *httpapi.Server
	service := fxtest.New(t, bootstrap.Options(cfg), fx.Populate(&public))
	service.RequireStart()
	base := "http://" + public.Addr()

	tests := []struct {
		name   string
		path   string
		bearer string
		want   response
	}{
		{"health stays public", "/health/ready", "", response{status: http.StatusOK}},
		{"no token", "/wallets/0192f291-27dd-7d3f-8071-5f8685deef37", "", response{
			status: http.StatusUnauthorized, challenge: `Bearer realm="wagering"`, code: "UNAUTHENTICATED",
		}},
		{"invalid token", "/wallets/0192f291-27dd-7d3f-8071-5f8685deef37", forged, response{
			status:    http.StatusUnauthorized,
			challenge: `Bearer realm="wagering", error="invalid_token", error_description="the access token is invalid or expired"`,
			code:      "UNAUTHENTICATED",
		}},
		{"unknown path without a token", "/admin", "", response{
			status: http.StatusUnauthorized, challenge: `Bearer realm="wagering"`, code: "UNAUTHENTICATED",
		}},
		{"authenticated, no route yet", "/wallets/0192f291-27dd-7d3f-8071-5f8685deef37", valid, response{
			status: http.StatusNotFound, code: "NOT_FOUND",
		}},
	}
	for _, tt := range tests {
		if got := get(t, base+tt.path, tt.bearer); got != tt.want {
			t.Fatalf("%s: GET %s = %+v; want %+v", tt.name, tt.path, got, tt.want)
		}
	}
	service.RequireStop()
}
