package money_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

func TestMarshalJSON(t *testing.T) {
	got, err := json.Marshal(mustFromMinor(t, 2500, money.BRL))
	if err != nil || string(got) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("Marshal = %s, %v", got, err)
	}
	if _, err := json.Marshal(money.Money{}); !errors.Is(err, money.ErrUninitialized) {
		t.Fatalf("Marshal of zero value: err = %v", err)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	type bet struct {
		Stake money.Money `json:"stake"`
	}
	in := bet{Stake: mustFromMinor(t, 12345, money.USD)}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"stake":{"amount":"123.45","currency":"USD"}}`; string(data) != want {
		t.Fatalf("Marshal = %s; want %s", data, want)
	}
	var out bet
	if err := json.Unmarshal(data, &out); err != nil || out != in {
		t.Fatalf("Unmarshal = %+v, %v; want %+v", out, err, in)
	}
}

func TestUnmarshalJSON(t *testing.T) {
	tests := []struct {
		in    string
		minor int64
		err   error
	}{
		{`{"amount":"25.00","currency":"BRL"}`, 2500, nil},
		{`{"currency":"BRL","amount":"0.00"}`, 0, nil},
		{`{"amount":25.00,"currency":"BRL"}`, 0, money.ErrInvalidAmount},
		{`{"amount":"25","currency":"BRL"}`, 0, money.ErrInvalidAmount},
		{`{"amount":"-1.00","currency":"BRL"}`, 0, money.ErrInvalidAmount},
		{`{"amount":null,"currency":"BRL"}`, 0, money.ErrInvalidAmount},
		{`{"currency":"BRL"}`, 0, money.ErrInvalidAmount},
		{`null`, 0, money.ErrInvalidAmount},
		{`{"amount":"92233720368547758.08","currency":"BRL"}`, 0, money.ErrOverflow},
		{`{"amount":"25.00","currency":"brl"}`, 0, money.ErrInvalidCurrency},
		{`{"amount":"25.00","currency":986}`, 0, money.ErrInvalidCurrency},
		{`{"amount":"25.00"}`, 0, money.ErrInvalidCurrency},
		{`{"amount":"25.00","currency":"BRL","extra":1}`, 0, money.ErrInvalidJSON},
		{`"25.00"`, 0, money.ErrInvalidJSON},
		{`[]`, 0, money.ErrInvalidJSON},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var m money.Money
			err := json.Unmarshal([]byte(tt.in), &m)
			if tt.err != nil {
				var pe *money.ParseError
				if !errors.Is(err, tt.err) || !errors.As(err, &pe) || m != (money.Money{}) {
					t.Fatalf("got %v, %v; want error %v", m, err, tt.err)
				}
				return
			}
			if want := mustFromMinor(t, tt.minor, money.BRL); err != nil || m != want {
				t.Fatalf("got %v, %v; want %v", m, err, want)
			}
		})
	}
}
