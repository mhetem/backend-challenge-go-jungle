package money_test

import (
	"errors"
	"testing"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"0.00", "0.01", "25.00", "92233720368547758.07", "92233720368547758.08",
		"-1.00", "-0.00", "-92233720368547758.08", "1.5", "01.00", "1e2", "NaN", "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		unsigned, err := money.Parse(in, "BRL")
		signed, signedErr := money.ParseSigned(in, "BRL")
		if err == nil {
			if signedErr != nil || signed != unsigned {
				t.Fatalf("Parse(%q) = %v but ParseSigned = %v, %v", in, unsigned, signed, signedErr)
			}
		} else {
			var pe *money.ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("Parse(%q) error %v is not a *ParseError", in, err)
			}
			if negative, _ := signed.IsNegative(); signedErr == nil && !negative {
				t.Fatalf("Parse(%q) rejected %v, which ParseSigned accepts as non-negative", in, signed)
			}
		}
		if signedErr != nil {
			return
		}
		out := signed.String()
		again, err := money.ParseSigned(out, "BRL")
		if out != in || err != nil || again != signed {
			t.Fatalf("round trip of %q: String() = %q, reparsed as %v, %v", in, out, again, err)
		}
	})
}
