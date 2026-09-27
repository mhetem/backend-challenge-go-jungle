package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

func CanonicalPayload(p wager.Payload) []byte {
	return canonical(payloadFields(p))
}

func PayloadHash(p wager.Payload) string {
	return digest(CanonicalPayload(p))
}

func CanonicalMessage(cmd SubmitWager) []byte {
	fields := payloadFields(cmd.Payload)
	fields["idempotencyKey"] = cmd.IdempotencyKey
	return canonical(fields)
}

func MessageHash(cmd SubmitWager) string {
	return digest(CanonicalMessage(cmd))
}

func payloadFields(p wager.Payload) map[string]any {
	cur, _ := p.Money.Currency()
	fields := map[string]any{
		"providerId":            p.ProviderID,
		"externalTransactionId": p.ExternalTransactionID,
		"playerId":              p.PlayerID.String(),
		"walletId":              p.WalletID.String(),
		"roundId":               p.RoundID,
		"gameId":                p.GameID,
		"kind":                  string(p.Kind),
		"money":                 map[string]string{"amount": p.Money.String(), "currency": string(cur)},
	}
	if p.ReferenceExternalTransactionID != "" {
		fields["referenceExternalTransactionId"] = p.ReferenceExternalTransactionID
	}
	return fields
}

func canonical(fields map[string]any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(fields)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
