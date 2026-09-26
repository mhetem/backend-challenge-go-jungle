package app_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

func TestCommandFromHTTP(t *testing.T) {
	cmd, err := bet().Command("provider-a:transaction-123", "corr-1")
	if err != nil {
		t.Fatal(err)
	}
	amount, err := money.Parse("25.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	want := app.SubmitWager{
		Payload: wager.Payload{
			ProviderID:            "provider-a",
			ExternalTransactionID: "transaction-123",
			PlayerID:              uuid.MustParse(playerID),
			WalletID:              uuid.MustParse(walletID),
			RoundID:               "round-987",
			GameID:                "fortune-chimp",
			Kind:                  wager.Bet,
			Money:                 amount,
		},
		IdempotencyKey: "provider-a:transaction-123",
		PayloadHash:    betHash,
		CorrelationID:  "corr-1",
		Channel:        app.ChannelHTTP,
	}
	if cmd != want {
		t.Fatalf("Command = %+v; want %+v", cmd, want)
	}
	tx, err := wager.NewExternal(wager.ExternalParams{
		Payload:        cmd.Payload,
		ID:             uuid.MustParse("0192f298-345e-7e38-af88-e43f851a819d"),
		IdempotencyKey: cmd.IdempotencyKey,
		PayloadHash:    cmd.PayloadHash,
		CorrelationID:  cmd.CorrelationID,
	}, time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC))
	if err != nil || tx.Snapshot().PayloadHash != betHash {
		t.Fatalf("NewExternal from the command = %v, %v", tx, err)
	}
}

func TestCommandFromSQS(t *testing.T) {
	data := app.WagerTransactionRequestedData{WagerRequest: bet(), IdempotencyKey: "provider-a:transaction-123"}
	cmd, err := data.Command("msg-123")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.IdempotencyKey != "provider-a:transaction-123" || cmd.CorrelationID != "msg-123" || cmd.Channel != app.ChannelSQS {
		t.Fatalf("Command = %+v", cmd)
	}
}

type input struct {
	request app.WagerRequest
	key     string
	corr    string
}

func TestCommandValidation(t *testing.T) {
	long := strings.Repeat("x", 129)
	tests := []struct {
		name   string
		mutate func(*input)
		want   string
	}{
		{"missing provider", func(in *input) { in.request.ProviderID = "" }, "providerId: REQUIRED"},
		{"provider with a space", func(in *input) { in.request.ProviderID = "provider a" }, "providerId: INVALID_VALUE"},
		{"missing external id", func(in *input) { in.request.ExternalTransactionID = "" }, "externalTransactionId: REQUIRED"},
		{"external id too long", func(in *input) { in.request.ExternalTransactionID = long }, "externalTransactionId: INVALID_VALUE"},
		{"missing player", func(in *input) { in.request.PlayerID = "" }, "playerId: REQUIRED"},
		{"malformed player", func(in *input) { in.request.PlayerID = "not-a-uuid" }, "playerId: INVALID_VALUE"},
		{"player without hyphens", func(in *input) { in.request.PlayerID = strings.ReplaceAll(playerID, "-", "") }, "playerId: INVALID_VALUE"},
		{"player in braces", func(in *input) { in.request.PlayerID = "{" + playerID + "}" }, "playerId: INVALID_VALUE"},
		{"missing wallet", func(in *input) { in.request.WalletID = "" }, "walletId: REQUIRED"},
		{"wallet as a URN", func(in *input) { in.request.WalletID = "urn:uuid:" + walletID }, "walletId: INVALID_VALUE"},
		{"nil wallet", func(in *input) { in.request.WalletID = uuid.Nil.String() }, "walletId: INVALID_VALUE"},
		{"missing round", func(in *input) { in.request.RoundID = "" }, "roundId: REQUIRED"},
		{"control character in round", func(in *input) { in.request.RoundID = "round\n987" }, "roundId: INVALID_VALUE"},
		{"missing game", func(in *input) { in.request.GameID = "" }, "gameId: REQUIRED"},
		{"non-ASCII game", func(in *input) { in.request.GameID = "fortune-chimpé" }, "gameId: INVALID_VALUE"},
		{"missing kind", func(in *input) { in.request.Kind = "" }, "kind: REQUIRED"},
		{"lowercase kind", func(in *input) { in.request.Kind = "bet" }, "kind: INVALID_VALUE"},
		{"unknown kind", func(in *input) { in.request.Kind = "JACKPOT" }, "kind: INVALID_VALUE"},
		{"opening", func(in *input) { in.request.Kind = "OPENING" }, "kind: KIND_NOT_ALLOWED"},
		{"missing money", func(in *input) { in.request.Money = app.MoneyRequest{} }, "money.amount: REQUIRED\nmoney.currency: REQUIRED"},
		{"amount without cents", func(in *input) { in.request.Money.Amount = "25" }, "money.amount: INVALID_AMOUNT"},
		{"negative amount", func(in *input) { in.request.Money.Amount = "-25.00" }, "money.amount: INVALID_AMOUNT"},
		{"scientific amount", func(in *input) { in.request.Money.Amount = "1e2" }, "money.amount: INVALID_AMOUNT"},
		{"overflowing amount", func(in *input) { in.request.Money.Amount = "92233720368547758.08" }, "money.amount: INVALID_AMOUNT"},
		{"lowercase currency", func(in *input) { in.request.Money.Currency = "brl" }, "money.currency: INVALID_VALUE"},
		{"unsupported currency", func(in *input) { in.request.Money.Currency = "JPY" }, "money.currency: INVALID_VALUE"},
		{"zero bet", func(in *input) { in.request.Money.Amount = "0.00" }, "money: INVALID_AMOUNT"},
		{"zero win", func(in *input) { in.request.Kind, in.request.Money.Amount = "WIN", "0.00" }, "money: INVALID_AMOUNT"},
		{"loss with an amount", func(in *input) { in.request.Kind = "LOSS" }, "money: INVALID_AMOUNT"},
		{"zero refund", func(in *input) {
			in.request.Kind, in.request.Money.Amount, in.request.ReferenceExternalTransactionID = "REFUND", "0.00", "transaction-122"
		}, "money: INVALID_AMOUNT"},
		{"zero rollback", func(in *input) {
			in.request.Kind, in.request.Money.Amount, in.request.ReferenceExternalTransactionID = "ROLLBACK", "0.00", "transaction-122"
		}, "money: INVALID_AMOUNT"},
		{"refund without reference", func(in *input) { in.request.Kind = "REFUND" }, "referenceExternalTransactionId: REQUIRED"},
		{"rollback without reference", func(in *input) { in.request.Kind = "ROLLBACK" }, "referenceExternalTransactionId: REQUIRED"},
		{"bet with reference", func(in *input) {
			in.request.ReferenceExternalTransactionID = "transaction-122"
		}, "referenceExternalTransactionId: INVALID_VALUE"},
		{"loss with reference", func(in *input) {
			in.request.Kind, in.request.Money.Amount, in.request.ReferenceExternalTransactionID = "LOSS", "0.00", "transaction-122"
		}, "referenceExternalTransactionId: INVALID_VALUE"},
		{"win referencing itself", func(in *input) {
			in.request.Kind, in.request.ReferenceExternalTransactionID = "WIN", in.request.ExternalTransactionID
		}, "referenceExternalTransactionId: INVALID_VALUE"},
		{"malformed reference", func(in *input) {
			in.request.Kind, in.request.ReferenceExternalTransactionID = "REFUND", "transaction 122"
		}, "referenceExternalTransactionId: INVALID_VALUE"},
		{"missing key", func(in *input) { in.key = "" }, "idempotencyKey: REQUIRED"},
		{"key too long", func(in *input) { in.key = long }, "idempotencyKey: INVALID_VALUE"},
		{"key with a space", func(in *input) { in.key = "provider-a: transaction-123" }, "idempotencyKey: INVALID_VALUE"},
		{"missing correlation", func(in *input) { in.corr = "" }, "correlationId: REQUIRED"},
		{"format errors come before rules", func(in *input) {
			in.request.Kind, in.request.PlayerID = "OPENING", "not-a-uuid"
		}, "playerId: INVALID_VALUE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input{request: bet(), key: "provider-a:transaction-123", corr: "corr-1"}
			tt.mutate(&in)
			cmd, err := in.request.Command(in.key, in.corr)
			if err == nil || err.Error() != tt.want || cmd != (app.SubmitWager{}) {
				t.Fatalf("Command = %+v, %v; want error %q", cmd, err, tt.want)
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Category != domain.Invalid {
				t.Fatalf("error %v is not a domain Invalid error", err)
			}
		})
	}
}

func TestCommandReportsEveryMissingField(t *testing.T) {
	_, err := app.WagerRequest{}.Command("", "")
	want := "providerId: REQUIRED\nexternalTransactionId: REQUIRED\nplayerId: REQUIRED\nwalletId: REQUIRED\n" +
		"roundId: REQUIRED\ngameId: REQUIRED\nkind: REQUIRED\nmoney.amount: REQUIRED\nmoney.currency: REQUIRED\n" +
		"idempotencyKey: REQUIRED\ncorrelationId: REQUIRED"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v; want %q", err, want)
	}
}

func TestCommandAcceptsEdgeTokens(t *testing.T) {
	edge := strings.Repeat("~", 127) + "!"
	r := bet()
	r.ProviderID, r.ExternalTransactionID, r.RoundID, r.GameID = edge, edge, "<&>", `"\`
	r.PlayerID = strings.ToUpper(playerID)
	cmd, err := r.Command(edge, edge)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.PlayerID != uuid.MustParse(playerID) || cmd.IdempotencyKey != edge || cmd.ExternalTransactionID != edge {
		t.Fatalf("Command = %+v", cmd)
	}
}
