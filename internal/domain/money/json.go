package money

import (
	"bytes"
	"encoding/json"
)

type wireMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	if !m.valid() {
		return nil, ErrUninitialized
	}
	return json.Marshal(wireMoney{Amount: m.String(), Currency: string(m.cur)})
}

func (m *Money) UnmarshalJSON(data []byte) error {
	var w struct {
		Amount   json.RawMessage `json:"amount"`
		Currency json.RawMessage `json:"currency"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return &ParseError{Input: string(data), Reason: ErrInvalidJSON}
	}
	amount, err := decodeString(w.Amount, ErrInvalidAmount)
	if err != nil {
		return err
	}
	currency, err := decodeString(w.Currency, ErrInvalidCurrency)
	if err != nil {
		return err
	}
	parsed, err := Parse(amount, currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

func decodeString(raw json.RawMessage, reason error) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ParseError{Input: string(raw), Reason: reason}
	}
	return s, nil
}
