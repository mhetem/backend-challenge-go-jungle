package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
)

func CanonicalPayload(p wager.Payload) []byte {
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
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(fields)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func PayloadHash(p wager.Payload) string {
	sum := sha256.Sum256(CanonicalPayload(p))
	return hex.EncodeToString(sum[:])
}
