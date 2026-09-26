package domain

import (
	"errors"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

type Category string

const (
	Invalid  Category = "INVALID"
	Rejected Category = "REJECTED"
	Conflict Category = "CONFLICT"
	NotFound Category = "NOT_FOUND"
)

type Error struct {
	Code     string
	Category Category
}

func (e *Error) Error() string {
	return e.Code
}

var (
	ErrUninitialized       = &Error{Code: "UNINITIALIZED", Category: Invalid}
	ErrRequired            = &Error{Code: "REQUIRED", Category: Invalid}
	ErrInvalidValue        = &Error{Code: "INVALID_VALUE", Category: Invalid}
	ErrInvalidAmount       = &Error{Code: "INVALID_AMOUNT", Category: Invalid}
	ErrKindNotAllowed      = &Error{Code: "KIND_NOT_ALLOWED", Category: Invalid}
	ErrInsufficientFunds   = &Error{Code: "INSUFFICIENT_FUNDS", Category: Rejected}
	ErrBalanceOverflow     = &Error{Code: "BALANCE_OVERFLOW", Category: Rejected}
	ErrCurrencyMismatch    = &Error{Code: "CURRENCY_MISMATCH", Category: Rejected}
	ErrTerminalState       = &Error{Code: "TERMINAL_STATE", Category: Conflict}
	ErrInvalidTransition   = &Error{Code: "INVALID_TRANSITION", Category: Conflict}
	ErrWalletExists        = &Error{Code: "WALLET_ALREADY_EXISTS", Category: Conflict}
	ErrWalletNotFound      = &Error{Code: "WALLET_NOT_FOUND", Category: NotFound}
	ErrTransactionNotFound = &Error{Code: "TRANSACTION_NOT_FOUND", Category: NotFound}
)

type FieldError struct {
	Field string
	Err   error
}

func NewFieldError(field string, err error) *FieldError {
	return &FieldError{Field: field, Err: err}
}

func (e *FieldError) Error() string {
	return e.Field + ": " + e.Err.Error()
}

func (e *FieldError) Unwrap() error {
	return e.Err
}

type Validation []error

func (v *Validation) Add(field string, err error) {
	*v = append(*v, NewFieldError(field, err))
}

func (v *Validation) Check(ok bool, field string, err error) {
	if !ok {
		v.Add(field, err)
	}
}

func (v *Validation) CheckAmount(field string, m money.Money, accept func(money.Money) (bool, error)) {
	ok, err := accept(m)
	switch {
	case err != nil:
		v.Add(field, ErrRequired)
	case !ok:
		v.Add(field, ErrInvalidAmount)
	}
}

func (v Validation) Err() error {
	return errors.Join(v...)
}

func NonNegative(m money.Money) (bool, error) {
	negative, err := m.IsNegative()
	return !negative, err
}
