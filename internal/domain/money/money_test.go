package money_test

import (
	"errors"
	"math"
	"testing"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

type parseCase struct {
	in    string
	minor int64
	err   error
}

func runParseCases(t *testing.T, parse func(string, string) (money.Money, error), cases []parseCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			m, err := parse(tc.in, "BRL")
			if tc.err != nil {
				var pe *money.ParseError
				if !errors.Is(err, tc.err) || !errors.As(err, &pe) || pe.Input != tc.in {
					t.Fatalf("got %v, %v; want error %v", m, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			minor, _ := m.Minor()
			cur, _ := m.Currency()
			if minor != tc.minor || cur != money.BRL || m.String() != tc.in {
				t.Fatalf("got %d %s (%s); want %d BRL", minor, cur, m, tc.minor)
			}
		})
	}
}

func mustFromMinor(t *testing.T, minor int64, cur money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(minor, cur)
	if err != nil {
		t.Fatalf("FromMinor(%d, %s): %v", minor, cur, err)
	}
	return m
}

func TestParse(t *testing.T) {
	runParseCases(t, money.Parse, []parseCase{
		{"0.00", 0, nil},
		{"0.01", 1, nil},
		{"0.50", 50, nil},
		{"1.00", 100, nil},
		{"25.00", 2500, nil},
		{"1000000.99", 100000099, nil},
		{"92233720368547758.07", math.MaxInt64, nil},
		{"92233720368547758.08", 0, money.ErrOverflow},
		{"92233720368547759.00", 0, money.ErrOverflow},
		{"100000000000000000.00", 0, money.ErrOverflow},
		{"", 0, money.ErrInvalidAmount},
		{"NaN", 0, money.ErrInvalidAmount},
		{"Infinity", 0, money.ErrInvalidAmount},
		{"1e2", 0, money.ErrInvalidAmount},
		{"1.234", 0, money.ErrInvalidAmount},
		{"1.5", 0, money.ErrInvalidAmount},
		{"1.", 0, money.ErrInvalidAmount},
		{".50", 0, money.ErrInvalidAmount},
		{"1", 0, money.ErrInvalidAmount},
		{"-1.00", 0, money.ErrInvalidAmount},
		{"-0.00", 0, money.ErrInvalidAmount},
		{"+1.00", 0, money.ErrInvalidAmount},
		{" 1.00", 0, money.ErrInvalidAmount},
		{"1.00 ", 0, money.ErrInvalidAmount},
		{"1.00\n", 0, money.ErrInvalidAmount},
		{"01.00", 0, money.ErrInvalidAmount},
		{"00.00", 0, money.ErrInvalidAmount},
		{"1,00", 0, money.ErrInvalidAmount},
		{"1_000.00", 0, money.ErrInvalidAmount},
		{"١.٠٠", 0, money.ErrInvalidAmount},
	})
}

func TestParseSigned(t *testing.T) {
	runParseCases(t, money.ParseSigned, []parseCase{
		{"0.00", 0, nil},
		{"1.00", 100, nil},
		{"-0.01", -1, nil},
		{"-25.50", -2550, nil},
		{"92233720368547758.07", math.MaxInt64, nil},
		{"-92233720368547758.08", math.MinInt64, nil},
		{"92233720368547758.08", 0, money.ErrOverflow},
		{"-92233720368547758.09", 0, money.ErrOverflow},
		{"-0.00", 0, money.ErrInvalidAmount},
		{"--1.00", 0, money.ErrInvalidAmount},
		{"+1.00", 0, money.ErrInvalidAmount},
		{"-01.00", 0, money.ErrInvalidAmount},
		{"- 1.00", 0, money.ErrInvalidAmount},
		{"-", 0, money.ErrInvalidAmount},
		{"-1.5", 0, money.ErrInvalidAmount},
	})
}

func TestParseRejectsInvalidCurrency(t *testing.T) {
	for _, parse := range []func(string, string) (money.Money, error){money.Parse, money.ParseSigned} {
		_, err := parse("1.00", "brl")
		var pe *money.ParseError
		if !errors.Is(err, money.ErrInvalidCurrency) || !errors.As(err, &pe) || pe.Input != "brl" {
			t.Errorf("err = %v; want ErrInvalidCurrency for %q", err, "brl")
		}
	}
}

func TestParseCurrency(t *testing.T) {
	for _, code := range []string{"BRL", "EUR", "USD"} {
		if got, err := money.ParseCurrency(code); err != nil || string(got) != code {
			t.Errorf("ParseCurrency(%q) = %q, %v", code, got, err)
		}
	}
	for _, code := range []string{"", "brl", "Brl", "JPY", "XXX", "BRLX", " BRL"} {
		_, err := money.ParseCurrency(code)
		var pe *money.ParseError
		if !errors.Is(err, money.ErrInvalidCurrency) || !errors.As(err, &pe) || pe.Input != code {
			t.Errorf("ParseCurrency(%q) err = %v; want ErrInvalidCurrency", code, err)
		}
	}
}

func TestParseErrorMessage(t *testing.T) {
	_, err := money.Parse("1.5", "BRL")
	if err == nil {
		t.Fatal("Parse(1.5) succeeded")
	}
	if got, want := err.Error(), `money: invalid amount: "1.5"`; got != want {
		t.Errorf("Error() = %q; want %q", got, want)
	}
}

func TestFromMinor(t *testing.T) {
	m, err := money.FromMinor(-5, money.USD)
	if err != nil || m.String() != "-0.05" {
		t.Fatalf("FromMinor(-5, USD) = %s, %v", m, err)
	}
	if _, err := money.FromMinor(1, "usd"); !errors.Is(err, money.ErrInvalidCurrency) {
		t.Fatalf("FromMinor with lowercase currency: err = %v", err)
	}
}

func TestZero(t *testing.T) {
	m, err := money.Zero(money.EUR)
	if err != nil {
		t.Fatal(err)
	}
	if zero, err := m.IsZero(); !zero || err != nil || m.String() != "0.00" {
		t.Fatalf("Zero(EUR) = %s, zero=%v, %v", m, zero, err)
	}
	if _, err := money.Zero(""); !errors.Is(err, money.ErrInvalidCurrency) {
		t.Fatalf("Zero with empty currency: err = %v", err)
	}
}

func TestArithmetic(t *testing.T) {
	brl := func(minor int64) money.Money { return mustFromMinor(t, minor, money.BRL) }
	usd := mustFromMinor(t, 100, money.USD)
	add, sub := money.Money.Add, money.Money.Sub
	tests := []struct {
		name string
		op   func(money.Money, money.Money) (money.Money, error)
		a, b money.Money
		want int64
		err  error
	}{
		{"add", add, brl(100), brl(250), 350, nil},
		{"add zero", add, brl(100), brl(0), 100, nil},
		{"add negative", add, brl(-100), brl(50), -50, nil},
		{"add extremes", add, brl(math.MaxInt64), brl(math.MinInt64), -1, nil},
		{"add overflow", add, brl(math.MaxInt64), brl(1), 0, money.ErrOverflow},
		{"add underflow", add, brl(math.MinInt64), brl(-1), 0, money.ErrOverflow},
		{"add currency mismatch", add, brl(100), usd, 0, money.ErrCurrencyMismatch},
		{"add to uninitialized", add, money.Money{}, brl(1), 0, money.ErrUninitialized},
		{"add uninitialized", add, brl(1), money.Money{}, 0, money.ErrUninitialized},
		{"sub", sub, brl(350), brl(100), 250, nil},
		{"sub below zero", sub, brl(100), brl(250), -150, nil},
		{"sub down to min", sub, brl(-1), brl(math.MaxInt64), math.MinInt64, nil},
		{"sub underflow", sub, brl(math.MinInt64), brl(1), 0, money.ErrOverflow},
		{"sub overflow", sub, brl(math.MaxInt64), brl(-1), 0, money.ErrOverflow},
		{"sub min from zero", sub, brl(0), brl(math.MinInt64), 0, money.ErrOverflow},
		{"sub currency mismatch", sub, usd, brl(100), 0, money.ErrCurrencyMismatch},
		{"sub from uninitialized", sub, money.Money{}, brl(1), 0, money.ErrUninitialized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.op(tt.a, tt.b)
			if tt.err != nil {
				if !errors.Is(err, tt.err) || got != (money.Money{}) {
					t.Fatalf("got %v, %v; want error %v", got, err, tt.err)
				}
				return
			}
			if want := mustFromMinor(t, tt.want, money.BRL); err != nil || got != want {
				t.Fatalf("got %v, %v; want %v", got, err, want)
			}
		})
	}
}

func TestNeg(t *testing.T) {
	tests := []struct {
		in   int64
		want int64
		err  error
	}{
		{100, -100, nil},
		{-100, 100, nil},
		{0, 0, nil},
		{math.MaxInt64, -math.MaxInt64, nil},
		{math.MinInt64, 0, money.ErrOverflow},
	}
	for _, tt := range tests {
		got, err := mustFromMinor(t, tt.in, money.BRL).Neg()
		if tt.err != nil {
			if !errors.Is(err, tt.err) {
				t.Errorf("Neg(%d) err = %v; want %v", tt.in, err, tt.err)
			}
			continue
		}
		if want := mustFromMinor(t, tt.want, money.BRL); err != nil || got != want {
			t.Errorf("Neg(%d) = %v, %v; want %v", tt.in, got, err, want)
		}
	}
}

func TestCmp(t *testing.T) {
	one, two := mustFromMinor(t, 1, money.BRL), mustFromMinor(t, 2, money.BRL)
	tests := []struct {
		a, b money.Money
		want int
	}{
		{one, two, -1},
		{two, one, 1},
		{one, one, 0},
	}
	for _, tt := range tests {
		if got, err := tt.a.Cmp(tt.b); err != nil || got != tt.want {
			t.Errorf("%v.Cmp(%v) = %d, %v; want %d", tt.a, tt.b, got, err, tt.want)
		}
	}
	if _, err := one.Cmp(mustFromMinor(t, 1, money.USD)); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Cmp across currencies: err = %v", err)
	}
	if _, err := one.Cmp(money.Money{}); !errors.Is(err, money.ErrUninitialized) {
		t.Errorf("Cmp with zero value: err = %v", err)
	}
}

func TestSignPredicates(t *testing.T) {
	tests := []struct {
		minor    int64
		zero     bool
		positive bool
		negative bool
	}{
		{-1, false, false, true},
		{0, true, false, false},
		{1, false, true, false},
		{math.MinInt64, false, false, true},
		{math.MaxInt64, false, true, false},
	}
	for _, tt := range tests {
		m := mustFromMinor(t, tt.minor, money.BRL)
		zero, zeroErr := m.IsZero()
		positive, positiveErr := m.IsPositive()
		negative, negativeErr := m.IsNegative()
		if err := errors.Join(zeroErr, positiveErr, negativeErr); err != nil {
			t.Fatalf("%v: %v", m, err)
		}
		if zero != tt.zero || positive != tt.positive || negative != tt.negative {
			t.Errorf("%v: zero=%v positive=%v negative=%v", m, zero, positive, negative)
		}
	}
}

func TestZeroValueIsUninitialized(t *testing.T) {
	var m money.Money
	errs := map[string]error{}
	_, errs["Minor"] = m.Minor()
	_, errs["Currency"] = m.Currency()
	_, errs["Add"] = m.Add(m)
	_, errs["Sub"] = m.Sub(m)
	_, errs["Neg"] = m.Neg()
	_, errs["Cmp"] = m.Cmp(m)
	_, errs["IsZero"] = m.IsZero()
	_, errs["IsPositive"] = m.IsPositive()
	_, errs["IsNegative"] = m.IsNegative()
	_, errs["MarshalJSON"] = m.MarshalJSON()
	for name, err := range errs {
		if !errors.Is(err, money.ErrUninitialized) {
			t.Errorf("%s on zero value: err = %v; want ErrUninitialized", name, err)
		}
	}
	if got := m.String(); got != "<uninitialized>" {
		t.Errorf("String() on zero value = %q", got)
	}
}
