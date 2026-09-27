package app

import (
	"errors"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

type Channel string

const (
	ChannelHTTP Channel = "HTTP"
	ChannelSQS  Channel = "SQS"
)

const maxTokenLength = 128

type SubmitWager struct {
	wager.Payload
	IdempotencyKey string
	PayloadHash    string
	CorrelationID  string
	Channel        Channel
}

type MoneyRequest struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type WagerRequest struct {
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	Money                          MoneyRequest `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
}

type OpenWallet struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
	CorrelationID  string
}

type OpenWalletRequest struct {
	PlayerID       string       `json:"playerId"`
	InitialBalance MoneyRequest `json:"initialBalance"`
}

type WagerTransactionRequestedData struct {
	WagerRequest
	IdempotencyKey string `json:"idempotencyKey"`
}

func (r WagerRequest) Command(idempotencyKey, correlationID string) (SubmitWager, error) {
	return newSubmitWager(r, idempotencyKey, correlationID, ChannelHTTP)
}

func (d WagerTransactionRequestedData) Command(correlationID string) (SubmitWager, error) {
	return newSubmitWager(d.WagerRequest, d.IdempotencyKey, correlationID, ChannelSQS)
}

func (r OpenWalletRequest) Command(correlationID string) (OpenWallet, error) {
	var v domain.Validation
	playerID := parseUUID(&v, "playerId", r.PlayerID)
	initial := parseMoney(&v, "initialBalance", r.InitialBalance)
	checkToken(&v, "correlationId", correlationID)
	if err := v.Err(); err != nil {
		return OpenWallet{}, err
	}
	return OpenWallet{PlayerID: playerID, InitialBalance: initial, CorrelationID: correlationID}, nil
}

func newSubmitWager(r WagerRequest, idempotencyKey, correlationID string, channel Channel) (SubmitWager, error) {
	var v domain.Validation
	checkToken(&v, "providerId", r.ProviderID)
	checkToken(&v, "externalTransactionId", r.ExternalTransactionID)
	playerID := parseUUID(&v, "playerId", r.PlayerID)
	walletID := parseUUID(&v, "walletId", r.WalletID)
	checkToken(&v, "roundId", r.RoundID)
	checkToken(&v, "gameId", r.GameID)
	kind := parseKind(&v, r.Kind)
	amount := parseMoney(&v, "money", r.Money)
	if r.ReferenceExternalTransactionID != "" {
		checkToken(&v, "referenceExternalTransactionId", r.ReferenceExternalTransactionID)
	}
	checkToken(&v, "idempotencyKey", idempotencyKey)
	checkToken(&v, "correlationId", correlationID)
	if err := v.Err(); err != nil {
		return SubmitWager{}, err
	}
	p := wager.Payload{
		ProviderID:                     r.ProviderID,
		ExternalTransactionID:          r.ExternalTransactionID,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        r.RoundID,
		GameID:                         r.GameID,
		Kind:                           kind,
		Money:                          amount,
		ReferenceExternalTransactionID: r.ReferenceExternalTransactionID,
	}
	if err := p.Validate(); err != nil {
		return SubmitWager{}, err
	}
	return SubmitWager{
		Payload:        p,
		IdempotencyKey: idempotencyKey,
		PayloadHash:    PayloadHash(p),
		CorrelationID:  correlationID,
		Channel:        channel,
	}, nil
}

func checkToken(v *domain.Validation, field, s string) {
	if s == "" {
		v.Add(field, domain.ErrRequired)
		return
	}
	v.Check(validToken(s), field, domain.ErrInvalidValue)
}

func validToken(s string) bool {
	if len(s) > maxTokenLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '!' || s[i] > '~' {
			return false
		}
	}
	return true
}

func parseUUID(v *domain.Validation, field, s string) uuid.UUID {
	if s == "" {
		v.Add(field, domain.ErrRequired)
		return uuid.Nil
	}
	id, err := uuid.Parse(s)
	if len(s) != 36 || err != nil || id == uuid.Nil {
		v.Add(field, domain.ErrInvalidValue)
		return uuid.Nil
	}
	return id
}

func parseKind(v *domain.Validation, s string) wager.Kind {
	k := wager.Kind(s)
	switch {
	case s == "":
		v.Add("kind", domain.ErrRequired)
	case !k.Valid():
		v.Add("kind", domain.ErrInvalidValue)
	}
	return k
}

func parseMoney(v *domain.Validation, field string, m MoneyRequest) money.Money {
	v.Check(m.Amount != "", field+".amount", domain.ErrRequired)
	v.Check(m.Currency != "", field+".currency", domain.ErrRequired)
	if m.Amount == "" || m.Currency == "" {
		return money.Money{}
	}
	parsed, err := money.Parse(m.Amount, m.Currency)
	switch {
	case errors.Is(err, money.ErrInvalidCurrency):
		v.Add(field+".currency", domain.ErrInvalidValue)
	case err != nil:
		v.Add(field+".amount", domain.ErrInvalidAmount)
	}
	return parsed
}
