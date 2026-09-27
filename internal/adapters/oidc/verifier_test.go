package oidc_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/oidc"
	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
)

const (
	issuer   = "http://idp.test/realms/wagering"
	audience = "wagering-api"
)

type idp struct {
	mu     sync.Mutex
	keys   map[string]*rsa.PrivateKey
	server *httptest.Server
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	p := &idp{keys: map[string]*rsa.PrivateKey{}}
	p.rotate(t, "key-1")
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		var set jose.JSONWebKeySet
		for kid, key := range p.keys {
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: &key.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"})
		}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *idp) rotate(t *testing.T, kid string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys[kid] = key
}

func (p *idp) verifier() *oidc.Verifier {
	return oidc.New(issuer, p.server.URL, audience)
}

func accessClaims(overrides map[string]any) map[string]any {
	c := map[string]any{
		"iss":          issuer,
		"sub":          "service-account-provider-a",
		"aud":          []string{audience, "account"},
		"azp":          "provider-a",
		"typ":          "Bearer",
		"exp":          time.Now().Add(5 * time.Minute).Unix(),
		"iat":          time.Now().Unix(),
		"provider_id":  "provider-a",
		"realm_access": map[string]any{"roles": []string{"wager-provider", "default-roles-wagering"}},
	}
	maps.Copy(c, overrides)
	return c
}

func (p *idp) sign(t *testing.T, kid string, claims map[string]any) string {
	t.Helper()
	p.mu.Lock()
	key := p.keys[kid]
	p.mu.Unlock()
	return sign(t, jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: kid}}, claims)
}

func sign(t *testing.T, key jose.SigningKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(key, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func segment(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestValidAccessToken(t *testing.T) {
	p := newIDP(t)
	v := p.verifier()
	defer v.Stop()
	got, err := v.Authenticate(t.Context(), p.sign(t, "key-1", accessClaims(nil)))
	if err != nil {
		t.Fatal(err)
	}
	want := auth.Principal{
		Subject:    "service-account-provider-a",
		ClientID:   "provider-a",
		ProviderID: "provider-a",
		Roles:      []auth.Role{auth.WagerProvider, "default-roles-wagering"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("principal = %+v; want %+v", got, want)
	}
}

func TestKeysAreRefreshedOnAnUnknownKeyID(t *testing.T) {
	p := newIDP(t)
	v := p.verifier()
	defer v.Stop()
	if _, err := v.Authenticate(t.Context(), p.sign(t, "key-1", accessClaims(nil))); err != nil {
		t.Fatal(err)
	}
	p.rotate(t, "key-2")
	if _, err := v.Authenticate(t.Context(), p.sign(t, "key-2", accessClaims(nil))); err != nil {
		t.Fatalf("token signed with a rotated key: %v", err)
	}
}

func TestRejectedTokens(t *testing.T) {
	p := newIDP(t)
	v := p.verifier()
	defer v.Stop()
	valid := p.sign(t, "key-1", accessClaims(nil))
	parts := strings.Split(valid, ".")
	stranger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"empty":            "",
		"not a jwt":        "not-a-jwt",
		"expired":          p.sign(t, "key-1", accessClaims(map[string]any{"exp": time.Now().Add(-time.Second).Unix()})),
		"without expiry":   p.sign(t, "key-1", accessClaims(map[string]any{"exp": nil})),
		"other issuer":     p.sign(t, "key-1", accessClaims(map[string]any{"iss": "http://idp.test/realms/other"})),
		"other audience":   p.sign(t, "key-1", accessClaims(map[string]any{"aud": []string{"account"}})),
		"id token":         p.sign(t, "key-1", accessClaims(map[string]any{"typ": "ID"})),
		"refresh token":    p.sign(t, "key-1", accessClaims(map[string]any{"typ": "Refresh"})),
		"without azp":      p.sign(t, "key-1", accessClaims(map[string]any{"azp": nil})),
		"forged claims":    parts[0] + "." + segment(accessClaims(map[string]any{"provider_id": "provider-b"})) + "." + parts[2],
		"broken signature": parts[0] + "." + parts[1] + "." + parts[2][:40] + strings.Repeat("A", len(parts[2])-40),
		"unsigned":         segment(map[string]string{"alg": "none", "typ": "JWT"}) + "." + parts[1] + ".",
		"hmac":             sign(t, jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")}, accessClaims(nil)),
		"unknown signer":   sign(t, jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: stranger, KeyID: "key-1"}}, accessClaims(nil)),
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			principal, err := v.Authenticate(t.Context(), token)
			if !errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, auth.ErrUnavailable) {
				t.Fatalf("Authenticate = %+v, %v; want %v", principal, err, auth.ErrUnauthenticated)
			}
		})
	}
}

func TestUnreachableKeysAreUnavailableNotInvalid(t *testing.T) {
	p := newIDP(t)
	token := p.sign(t, "key-1", accessClaims(nil))
	p.server.Close()
	v := p.verifier()
	defer v.Stop()
	if _, err := v.Authenticate(t.Context(), token); !errors.Is(err, auth.ErrUnavailable) || errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Authenticate with the key endpoint down = %v; want %v", err, auth.ErrUnavailable)
	}
}
