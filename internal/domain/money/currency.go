package money

type Currency string

const (
	BRL Currency = "BRL"
	EUR Currency = "EUR"
	USD Currency = "USD"
)

func ParseCurrency(code string) (Currency, error) {
	c := Currency(code)
	if !c.Valid() {
		return "", &ParseError{Input: code, Reason: ErrInvalidCurrency}
	}
	return c, nil
}

func (c Currency) Valid() bool {
	switch c {
	case BRL, EUR, USD:
		return true
	}
	return false
}
