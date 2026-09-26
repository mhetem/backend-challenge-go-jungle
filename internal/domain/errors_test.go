package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		err      *domain.Error
		category domain.Category
	}{
		{domain.ErrUninitialized, domain.Invalid},
		{domain.ErrRequired, domain.Invalid},
		{domain.ErrInvalidValue, domain.Invalid},
		{domain.ErrInvalidAmount, domain.Invalid},
		{domain.ErrKindNotAllowed, domain.Invalid},
		{domain.ErrInsufficientFunds, domain.Rejected},
		{domain.ErrBalanceOverflow, domain.Rejected},
		{domain.ErrCurrencyMismatch, domain.Rejected},
		{domain.ErrTerminalState, domain.Conflict},
		{domain.ErrInvalidTransition, domain.Conflict},
		{domain.ErrWalletNotFound, domain.NotFound},
		{domain.ErrTransactionNotFound, domain.NotFound},
	}
	for _, tt := range tests {
		t.Run(tt.err.Code, func(t *testing.T) {
			err := errors.Join(domain.NewFieldError("field", fmt.Errorf("%w: detail", tt.err)))
			var de *domain.Error
			if !errors.Is(err, tt.err) || !errors.As(err, &de) || de.Category != tt.category {
				t.Fatalf("errors.As = %v; want category %s", de, tt.category)
			}
			var fe *domain.FieldError
			if !errors.As(err, &fe) || fe.Field != "field" {
				t.Fatalf("FieldError = %v", fe)
			}
			if got, want := err.Error(), "field: "+tt.err.Code+": detail"; got != want {
				t.Fatalf("Error() = %q; want %q", got, want)
			}
		})
	}
}

func TestValidation(t *testing.T) {
	var empty domain.Validation
	if err := empty.Err(); err != nil {
		t.Fatalf("empty Validation.Err() = %v", err)
	}
	one, err := money.Parse("1.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	negative, err := money.ParseSigned("-1.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	var v domain.Validation
	v.Check(true, "ok", domain.ErrRequired)
	v.Check(false, "id", domain.ErrRequired)
	v.CheckAmount("positive", one, money.Money.IsPositive)
	v.CheckAmount("zero", one, money.Money.IsZero)
	v.CheckAmount("missing", money.Money{}, domain.NonNegative)
	v.CheckAmount("negative", negative, domain.NonNegative)
	v.Add("kind", domain.ErrKindNotAllowed)
	err = v.Err()
	want := "id: REQUIRED\nzero: INVALID_AMOUNT\nmissing: REQUIRED\nnegative: INVALID_AMOUNT\nkind: KIND_NOT_ALLOWED"
	if err == nil || err.Error() != want {
		t.Fatalf("Err() = %v; want %q", err, want)
	}
	for _, sentinel := range []error{domain.ErrRequired, domain.ErrInvalidAmount, domain.ErrKindNotAllowed} {
		if !errors.Is(err, sentinel) {
			t.Errorf("errors.Is(err, %v) = false", sentinel)
		}
	}
}

func TestDeriveID(t *testing.T) {
	id := domain.DeriveID("opening", "0192f291-27dd-7d3f-8071-5f8685deef37")
	if again := domain.DeriveID("opening", "0192f291-27dd-7d3f-8071-5f8685deef37"); again != id {
		t.Fatalf("DeriveID is not deterministic: %s vs %s", id, again)
	}
	if id.Version() != 5 || id.Variant() != uuid.RFC4122 {
		t.Fatalf("DeriveID = %s; want an RFC 4122 version 5 UUID", id)
	}
	if other := domain.DeriveID("ledger", "0192f291-27dd-7d3f-8071-5f8685deef37"); other == id {
		t.Fatalf("different prefixes derived the same id %s", id)
	}
}
