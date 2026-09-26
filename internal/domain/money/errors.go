package money

import (
	"errors"
	"fmt"
)

var (
	ErrUninitialized    = errors.New("money: uninitialized value")
	ErrInvalidAmount    = errors.New("money: invalid amount")
	ErrInvalidCurrency  = errors.New("money: invalid currency")
	ErrInvalidJSON      = errors.New("money: invalid JSON")
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	ErrOverflow         = errors.New("money: overflow")
)

type ParseError struct {
	Input  string
	Reason error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%v: %q", e.Reason, e.Input)
}

func (e *ParseError) Unwrap() error {
	return e.Reason
}
