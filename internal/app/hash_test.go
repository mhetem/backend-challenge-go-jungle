package app_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
)

const (
	playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	walletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
	betHash  = "629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344"
)

func bet() app.WagerRequest {
	return app.WagerRequest{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 app.MoneyRequest{Amount: "25.00", Currency: "BRL"},
	}
}

func command(t *testing.T, r app.WagerRequest) app.SubmitWager {
	t.Helper()
	cmd, err := r.Command("provider-a:"+r.ExternalTransactionID, "corr-1")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	return cmd
}

func TestGoldenHashVectors(t *testing.T) {
	rollback := bet()
	rollback.ExternalTransactionID, rollback.Kind, rollback.ReferenceExternalTransactionID = "transaction-124", "ROLLBACK", "transaction-123"
	loss := bet()
	loss.ExternalTransactionID, loss.GameID, loss.RoundID, loss.Kind = "transaction-125", "slots<&>", `r"1\2`, "LOSS"
	loss.Money = app.MoneyRequest{Amount: "0.00", Currency: "USD"}
	tests := []struct {
		name      string
		request   app.WagerRequest
		canonical string
		hash      string
	}{
		{
			"bet",
			bet(),
			`{"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`,
			betHash,
		},
		{
			"rollback with reference",
			rollback,
			`{"externalTransactionId":"transaction-124","gameId":"fortune-chimp","kind":"ROLLBACK","money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","referenceExternalTransactionId":"transaction-123","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`,
			"68a4af5d43a96524de3addf2586eade9cbe04d895872a8bbf8da67cdf07cbc25",
		},
		{
			"loss with characters JSON escapes",
			loss,
			`{"externalTransactionId":"transaction-125","gameId":"slots<&>","kind":"LOSS","money":{"amount":"0.00","currency":"USD"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","roundId":"r\"1\\2","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`,
			"ca0de77e7b6194d24c8c2728fd90157903e1f89eb205d6065a0865cc8c06611f",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := command(t, tt.request)
			if got := string(app.CanonicalPayload(cmd.Payload)); got != tt.canonical {
				t.Fatalf("canonical payload\ngot  %s\nwant %s", got, tt.canonical)
			}
			if cmd.PayloadHash != tt.hash || app.PayloadHash(cmd.Payload) != tt.hash {
				t.Fatalf("hash = %s; want %s", cmd.PayloadHash, tt.hash)
			}
		})
	}
}

func TestUUIDsAreNormalized(t *testing.T) {
	upper := bet()
	upper.PlayerID, upper.WalletID = strings.ToUpper(playerID), strings.ToUpper(walletID)
	if got := command(t, upper).PayloadHash; got != betHash {
		t.Fatalf("hash of uppercase UUIDs = %s; want %s", got, betHash)
	}
}

const httpBody = `{
  "providerId": "provider-a",
  "externalTransactionId": "transaction-123",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "kind": "BET",
  "money": { "amount": "25.00", "currency": "BRL" }
}`

const sqsMessage = `{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}`

func decodeStrict(t *testing.T, data string, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestHTTPAndSQSHashIdentically(t *testing.T) {
	var body app.WagerRequest
	decodeStrict(t, httpBody, &body)
	var envelope struct {
		MessageID  string                            `json:"messageId"`
		Type       string                            `json:"type"`
		OccurredAt string                            `json:"occurredAt"`
		Data       app.WagerTransactionRequestedData `json:"data"`
	}
	decodeStrict(t, sqsMessage, &envelope)

	fromHTTP, err := body.Command("provider-a:transaction-123", "corr-http")
	if err != nil {
		t.Fatal(err)
	}
	fromSQS, err := envelope.Data.Command("corr-sqs")
	if err != nil {
		t.Fatal(err)
	}
	if fromHTTP.Payload != fromSQS.Payload || fromHTTP.PayloadHash != fromSQS.PayloadHash || fromHTTP.PayloadHash != betHash {
		t.Fatalf("HTTP hash %s, SQS hash %s; want both %s", fromHTTP.PayloadHash, fromSQS.PayloadHash, betHash)
	}
	if fromHTTP.IdempotencyKey != fromSQS.IdempotencyKey || fromHTTP.Channel != app.ChannelHTTP || fromSQS.Channel != app.ChannelSQS {
		t.Fatalf("HTTP %+v, SQS %+v", fromHTTP, fromSQS)
	}
}

func TestTransportMetadataDoesNotChangeHash(t *testing.T) {
	first, err := bet().Command("key-1", "corr-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.WagerTransactionRequestedData{WagerRequest: bet(), IdempotencyKey: "key-2"}.Command("corr-2")
	if err != nil {
		t.Fatal(err)
	}
	if first.IdempotencyKey == second.IdempotencyKey || first.PayloadHash != second.PayloadHash {
		t.Fatalf("keys %q and %q hash to %s and %s; want the same hash", first.IdempotencyKey, second.IdempotencyKey, first.PayloadHash, second.PayloadHash)
	}
}

func TestBusinessFieldsChangeHash(t *testing.T) {
	win := func() app.WagerRequest {
		r := bet()
		r.Kind = "WIN"
		return r
	}
	mutations := map[string]func(*app.WagerRequest){
		"providerId":            func(r *app.WagerRequest) { r.ProviderID = "provider-b" },
		"externalTransactionId": func(r *app.WagerRequest) { r.ExternalTransactionID = "transaction-124" },
		"playerId":              func(r *app.WagerRequest) { r.PlayerID = "0192f2a0-6e2f-7a19-8c5b-4d7f9e1a3c5b" },
		"walletId":              func(r *app.WagerRequest) { r.WalletID = "0192f2a0-51c3-7b8e-9f4d-2c6e8a0b1d3f" },
		"roundId":               func(r *app.WagerRequest) { r.RoundID = "round-988" },
		"gameId":                func(r *app.WagerRequest) { r.GameID = "fortune-tiger" },
		"kind":                  func(r *app.WagerRequest) { r.Kind = "BET" },
		"money.amount":          func(r *app.WagerRequest) { r.Money.Amount = "25.01" },
		"money.currency":        func(r *app.WagerRequest) { r.Money.Currency = "USD" },
		"referenceExternalTransactionId": func(r *app.WagerRequest) {
			r.ReferenceExternalTransactionID = "transaction-122"
		},
	}
	seen := map[string]string{command(t, win()).PayloadHash: "base"}
	for field, mutate := range mutations {
		r := win()
		mutate(&r)
		hash := command(t, r).PayloadHash
		if other, ok := seen[hash]; ok {
			t.Errorf("changing %s gives the same hash as %s", field, other)
		}
		seen[hash] = field
	}
}
