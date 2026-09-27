package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

var (
	providerA   = auth.Principal{ClientID: "provider-a", ProviderID: "provider-a", Roles: []auth.Role{auth.WagerProvider}}
	providerB   = auth.Principal{ClientID: "provider-b", ProviderID: "provider-b", Roles: []auth.Role{auth.WagerProvider}}
	operator    = auth.Principal{ClientID: "wallet-backoffice", Roles: []auth.Role{auth.WalletOperator}}
	noRole      = auth.Principal{ClientID: "no-role-client", ProviderID: "provider-a"}
	anonymousID = auth.Principal{ClientID: "provider-x", Roles: []auth.Role{auth.WagerProvider}}
)

func requireErr(t *testing.T, name string, got, want error) {
	t.Helper()
	if want == nil && got != nil || want != nil && !errors.Is(got, want) {
		t.Fatalf("%s = %v; want %v", name, got, want)
	}
}

func TestSubmitWager(t *testing.T) {
	tests := []struct {
		name       string
		principal  auth.Principal
		providerID string
		want       error
	}{
		{"own provider", providerA, "provider-a", nil},
		{"another provider", providerB, "provider-a", auth.ErrForbidden},
		{"operator", operator, "provider-a", auth.ErrForbidden},
		{"no role, even with a provider claim", noRole, "provider-a", auth.ErrForbidden},
		{"provider role without a provider claim", anonymousID, "", auth.ErrForbidden},
	}
	for _, tt := range tests {
		requireErr(t, tt.name, auth.SubmitWager(tt.principal, tt.providerID), tt.want)
	}
}

func TestReadProviderTransaction(t *testing.T) {
	tests := []struct {
		name       string
		principal  auth.Principal
		providerID string
		want       error
	}{
		{"own provider", providerA, "provider-a", nil},
		{"another provider", providerB, "provider-a", auth.ErrForbidden},
		{"operator", operator, "provider-a", nil},
		{"no role", noRole, "provider-a", auth.ErrForbidden},
	}
	for _, tt := range tests {
		requireErr(t, tt.name, auth.ReadProviderTransaction(tt.principal, tt.providerID), tt.want)
	}
}

func TestReadTransaction(t *testing.T) {
	ofA := wager.Snapshot{ProviderID: "provider-a"}
	opening := wager.Snapshot{}
	tests := []struct {
		name      string
		principal auth.Principal
		tx        wager.Snapshot
		want      error
	}{
		{"own transaction", providerA, ofA, nil},
		{"another provider's transaction is hidden", providerB, ofA, domain.ErrTransactionNotFound},
		{"internal opening is hidden from providers", providerA, opening, domain.ErrTransactionNotFound},
		{"operator reads any transaction", operator, ofA, nil},
		{"operator reads the opening", operator, opening, nil},
		{"no role", noRole, ofA, domain.ErrTransactionNotFound},
	}
	for _, tt := range tests {
		requireErr(t, tt.name, auth.ReadTransaction(tt.principal, tt.tx), tt.want)
	}
	requireErr(t, "provider before the lookup", auth.ReadTransactions(providerB), nil)
	requireErr(t, "operator before the lookup", auth.ReadTransactions(operator), nil)
	requireErr(t, "no role before the lookup", auth.ReadTransactions(noRole), auth.ErrForbidden)
}

func TestManageWallets(t *testing.T) {
	requireErr(t, "operator", auth.ManageWallets(operator), nil)
	for _, p := range []auth.Principal{providerA, noRole} {
		requireErr(t, p.ClientID, auth.ManageWallets(p), auth.ErrForbidden)
	}
}

func TestPrincipalTravelsInTheContext(t *testing.T) {
	if _, ok := auth.PrincipalFrom(context.Background()); ok {
		t.Fatal("background context carries a principal")
	}
	got, ok := auth.PrincipalFrom(auth.WithPrincipal(context.Background(), providerA))
	if !ok || got.ClientID != "provider-a" || !got.Has(auth.WagerProvider) || got.Has(auth.WalletOperator) {
		t.Fatalf("PrincipalFrom = %+v, %v", got, ok)
	}
}
