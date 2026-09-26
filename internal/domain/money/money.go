package money

import (
	"cmp"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var (
	unsignedAmount = regexp.MustCompile(`^(0|[1-9]\d*)\.\d{2}$`)
	signedAmount   = regexp.MustCompile(`^-?(0|[1-9]\d*)\.\d{2}$`)
)

type Money struct {
	minor int64
	cur   Currency
}

func Zero(cur Currency) (Money, error) {
	return FromMinor(0, cur)
}

func FromMinor(minor int64, cur Currency) (Money, error) {
	if !cur.Valid() {
		return Money{}, &ParseError{Input: string(cur), Reason: ErrInvalidCurrency}
	}
	return Money{minor: minor, cur: cur}, nil
}

func Parse(amount, currency string) (Money, error) {
	return parse(amount, currency, unsignedAmount)
}

func ParseSigned(amount, currency string) (Money, error) {
	return parse(amount, currency, signedAmount)
}

func parse(amount, currency string, pattern *regexp.Regexp) (Money, error) {
	cur, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	if !pattern.MatchString(amount) {
		return Money{}, &ParseError{Input: amount, Reason: ErrInvalidAmount}
	}
	digits, negative := strings.CutPrefix(amount, "-")
	limit := uint64(math.MaxInt64)
	if negative {
		limit++
	}
	var n uint64
	for _, c := range digits {
		if c == '.' {
			continue
		}
		d := uint64(c - '0')
		if n > (limit-d)/10 {
			return Money{}, &ParseError{Input: amount, Reason: ErrOverflow}
		}
		n = n*10 + d
	}
	switch {
	case !negative:
		return Money{minor: int64(n), cur: cur}, nil
	case n == 0:
		return Money{}, &ParseError{Input: amount, Reason: ErrInvalidAmount}
	default:
		return Money{minor: -int64(n), cur: cur}, nil
	}
}

func (m Money) Minor() (int64, error) {
	if !m.valid() {
		return 0, ErrUninitialized
	}
	return m.minor, nil
}

func (m Money) Currency() (Currency, error) {
	if !m.valid() {
		return "", ErrUninitialized
	}
	return m.cur, nil
}

func (m Money) Add(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	sum := m.minor + o.minor
	if (o.minor > 0 && sum < m.minor) || (o.minor < 0 && sum > m.minor) {
		return Money{}, ErrOverflow
	}
	return Money{minor: sum, cur: m.cur}, nil
}

func (m Money) Sub(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	diff := m.minor - o.minor
	if (o.minor > 0 && diff > m.minor) || (o.minor < 0 && diff < m.minor) {
		return Money{}, ErrOverflow
	}
	return Money{minor: diff, cur: m.cur}, nil
}

func (m Money) Neg() (Money, error) {
	if !m.valid() {
		return Money{}, ErrUninitialized
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{minor: -m.minor, cur: m.cur}, nil
}

func (m Money) Cmp(o Money) (int, error) {
	if err := m.compatible(o); err != nil {
		return 0, err
	}
	return cmp.Compare(m.minor, o.minor), nil
}

func (m Money) IsZero() (bool, error) {
	s, err := m.sign()
	return err == nil && s == 0, err
}

func (m Money) IsPositive() (bool, error) {
	s, err := m.sign()
	return err == nil && s > 0, err
}

func (m Money) IsNegative() (bool, error) {
	s, err := m.sign()
	return err == nil && s < 0, err
}

func (m Money) String() string {
	if !m.valid() {
		return "<uninitialized>"
	}
	prefix, abs := "", uint64(m.minor)
	if m.minor < 0 {
		prefix, abs = "-", -abs
	}
	return fmt.Sprintf("%s%d.%02d", prefix, abs/100, abs%100)
}

func (m Money) valid() bool {
	return m.cur.Valid()
}

func (m Money) compatible(o Money) error {
	if !m.valid() || !o.valid() {
		return ErrUninitialized
	}
	if m.cur != o.cur {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.cur, o.cur)
	}
	return nil
}

func (m Money) sign() (int, error) {
	if !m.valid() {
		return 0, ErrUninitialized
	}
	return cmp.Compare(m.minor, 0), nil
}
