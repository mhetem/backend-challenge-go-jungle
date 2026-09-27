package app_test

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
)

func TestLedgerRejectsBadPaging(t *testing.T) {
	s := app.NewWalletService(nil, app.SystemClock, app.NewID, slog.New(slog.DiscardHandler), nil)
	tests := []struct {
		name   string
		cursor string
		limit  int
		want   string
	}{
		{"cursor that is not base64url", "!!", 0, "cursor: INVALID_VALUE"},
		{"padded cursor", "MQ==", 0, "cursor: INVALID_VALUE"},
		{"cursor at version zero", "MA", 0, "cursor: INVALID_VALUE"},
		{"negative cursor", "LTE", 0, "cursor: INVALID_VALUE"},
		{"cursor with a leading zero", "MDE", 0, "cursor: INVALID_VALUE"},
		{"cursor with a sign", "KzE", 0, "cursor: INVALID_VALUE"},
		{"cursor with a space", "IDE", 0, "cursor: INVALID_VALUE"},
		{"negative limit", "", -1, "limit: INVALID_VALUE"},
		{"limit above the maximum", "", app.MaxLedgerLimit + 1, "limit: INVALID_VALUE"},
		{"both", "!!", app.MaxLedgerLimit + 1, "cursor: INVALID_VALUE\nlimit: INVALID_VALUE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := s.Ledger(t.Context(), uuid.New(), tt.cursor, tt.limit)
			if err == nil || err.Error() != tt.want || page.Entries != nil || page.NextCursor != "" {
				t.Fatalf("Ledger = %+v, %v; want error %q", page, err, tt.want)
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Category != domain.Invalid {
				t.Fatalf("error %v is not a domain Invalid error", err)
			}
		})
	}
}

func TestWalletExistsError(t *testing.T) {
	id := uuid.MustParse(walletID)
	var err error = &app.WalletExistsError{WalletID: id}
	var exists *app.WalletExistsError
	var de *domain.Error
	if !errors.Is(err, domain.ErrWalletExists) || !errors.As(err, &exists) || exists.WalletID != id ||
		!errors.As(err, &de) || de.Category != domain.Conflict {
		t.Fatalf("%v does not classify as %v carrying %s", err, domain.ErrWalletExists, id)
	}
	if want := "WALLET_ALREADY_EXISTS: wallet " + walletID; err.Error() != want {
		t.Fatalf("Error() = %q; want %q", err.Error(), want)
	}
}
