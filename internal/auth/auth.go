package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

type Role string

const (
	WagerProvider  Role = "wager-provider"
	WalletOperator Role = "wallet-operator"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrUnavailable     = errors.New("identity provider unavailable")
	ErrForbidden       = errors.New("forbidden")
)

type Principal struct {
	Subject    string
	ClientID   string
	ProviderID string
	Roles      []Role
}

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Principal, error)
}

func (p Principal) Has(r Role) bool {
	return slices.Contains(p.Roles, r)
}

func (p Principal) provider() bool {
	return p.Has(WagerProvider) && p.ProviderID != ""
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func SubmitWager(p Principal, providerID string) error {
	switch {
	case !p.provider():
		return fmt.Errorf("%w: submitting wagers requires the %s role and a provider identity", ErrForbidden, WagerProvider)
	case p.ProviderID != providerID:
		return fmt.Errorf("%w: client %s acts for %s, not %s", ErrForbidden, p.ClientID, p.ProviderID, providerID)
	}
	return nil
}

func ReadProviderTransaction(p Principal, providerID string) error {
	switch {
	case p.Has(WalletOperator):
		return nil
	case !p.provider():
		return fmt.Errorf("%w: reading transactions requires the %s or %s role", ErrForbidden, WagerProvider, WalletOperator)
	case p.ProviderID != providerID:
		return fmt.Errorf("%w: client %s acts for %s, not %s", ErrForbidden, p.ClientID, p.ProviderID, providerID)
	}
	return nil
}

func ReadTransactions(p Principal) error {
	if p.Has(WalletOperator) || p.provider() {
		return nil
	}
	return fmt.Errorf("%w: reading transactions requires the %s or %s role", ErrForbidden, WagerProvider, WalletOperator)
}

func ReadTransaction(p Principal, tx wager.Snapshot) error {
	if p.Has(WalletOperator) || p.provider() && tx.ProviderID == p.ProviderID {
		return nil
	}
	return domain.ErrTransactionNotFound
}

func ManageWallets(p Principal) error {
	if p.Has(WalletOperator) {
		return nil
	}
	return fmt.Errorf("%w: wallet operations require the %s role", ErrForbidden, WalletOperator)
}
