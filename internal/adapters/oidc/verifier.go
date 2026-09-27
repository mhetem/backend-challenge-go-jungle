package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
)

const fetchTimeout = 5 * time.Second

var Module = fx.Module("oidc",
	fx.Provide(
		func(cfg config.Config) *Verifier { return New(cfg.OIDC.Issuer, cfg.OIDC.JWKSURL, cfg.OIDC.Audience) },
		func(v *Verifier) auth.Authenticator { return v },
	),
	fx.Invoke(func(lc fx.Lifecycle, v *Verifier) {
		lc.Append(fx.StopHook(v.Stop))
	}),
)

type Verifier struct {
	verifier  *gooidc.IDTokenVerifier
	transport *http.Transport
}

func New(issuer, jwksURL, audience string) *Verifier {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Transport: transport, Timeout: fetchTimeout}
	keys := gooidc.NewRemoteKeySet(gooidc.ClientContext(context.Background(), client), jwksURL)
	return &Verifier{
		verifier: gooidc.NewVerifier(issuer, keySet{keys}, &gooidc.Config{
			ClientID:             audience,
			SupportedSigningAlgs: []string{gooidc.RS256},
		}),
		transport: transport,
	}
}

func (v *Verifier) Stop() {
	v.transport.CloseIdleConnections()
}

type claims struct {
	Type            string `json:"typ"`
	AuthorizedParty string `json:"azp"`
	ProviderID      string `json:"provider_id"`
	RealmAccess     struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (v *Verifier) Authenticate(ctx context.Context, raw string) (auth.Principal, error) {
	var fetchErr error
	token, err := v.verifier.Verify(context.WithValue(ctx, fetchFailure{}, &fetchErr), raw)
	switch {
	case fetchErr != nil:
		return auth.Principal{}, fmt.Errorf("%w: %w", auth.ErrUnavailable, fetchErr)
	case err != nil:
		return auth.Principal{}, fmt.Errorf("%w: %w", auth.ErrUnauthenticated, err)
	}
	var c claims
	if err := token.Claims(&c); err != nil {
		return auth.Principal{}, fmt.Errorf("%w: %w", auth.ErrUnauthenticated, err)
	}
	if !strings.EqualFold(c.Type, "Bearer") || token.Subject == "" || c.AuthorizedParty == "" {
		return auth.Principal{}, fmt.Errorf("%w: not an access token (typ %q, sub %q, azp %q)", auth.ErrUnauthenticated, c.Type, token.Subject, c.AuthorizedParty)
	}
	roles := make([]auth.Role, 0, len(c.RealmAccess.Roles))
	for _, r := range c.RealmAccess.Roles {
		roles = append(roles, auth.Role(r))
	}
	return auth.Principal{
		Subject:    token.Subject,
		ClientID:   c.AuthorizedParty,
		ProviderID: c.ProviderID,
		Roles:      roles,
	}, nil
}

type fetchFailure struct{}

type keySet struct {
	remote gooidc.KeySet
}

func (k keySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	payload, err := k.remote.VerifySignature(ctx, jwt)
	if err != nil && errors.Unwrap(err) != nil {
		if slot, ok := ctx.Value(fetchFailure{}).(*error); ok {
			*slot = err
		}
	}
	return payload, err
}
