package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

type Wallets interface {
	Open(ctx context.Context, cmd app.OpenWallet) (wallet.Snapshot, error)
	Get(ctx context.Context, id uuid.UUID) (wallet.Snapshot, error)
	Ledger(ctx context.Context, walletID uuid.UUID, cursor string, limit int) (app.LedgerPage, error)
	Reconcile(ctx context.Context, walletID uuid.UUID) (app.Reconciliation, error)
}

type Wagers interface {
	Submit(ctx context.Context, cmd app.SubmitWager) (app.WagerResult, error)
	Get(ctx context.Context, id uuid.UUID) (wager.Snapshot, error)
	GetByExternalID(ctx context.Context, providerID, externalID string) (wager.Snapshot, error)
}

type api struct {
	wallets Wallets
	wagers  Wagers
	log     *slog.Logger
}

func (a *api) routes(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	handle := func(pattern string, h func(http.ResponseWriter, *http.Request) error) {
		mux.Handle(pattern, correlate(protect(a.endpoint(h))))
	}
	handle("POST /wallets", a.openWallet)
	handle("GET /wallets/{walletId}", a.getWallet)
	handle("GET /wallets/{walletId}/ledger", a.getLedger)
	handle("POST /wallets/{walletId}/reconciliation", a.reconcile)
	handle("POST /wagering/transactions", a.submitWager)
	handle("GET /wagering/transactions/{transactionId}", a.getTransaction)
	handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", a.getProviderTransaction)
	handle("/", func(http.ResponseWriter, *http.Request) error {
		return newProblem(http.StatusNotFound, "NOT_FOUND", categoryNotFound, "no such resource")
	})
}

func (a *api) endpoint(h func(http.ResponseWriter, *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := h(w, r)
		if err == nil {
			return
		}
		p := problemFor(err)
		switch {
		case p.Status >= http.StatusInternalServerError && !p.Retryable:
			a.log.ErrorContext(r.Context(), "request failed", "method", r.Method, "route", r.Pattern, "error", err)
		case p.Retryable:
			a.log.WarnContext(r.Context(), "request failed transiently", "method", r.Method, "route", r.Pattern, "error", err)
		}
		var exists *app.WalletExistsError
		if errors.As(err, &exists) {
			w.Header().Set("Location", "/wallets/"+exists.WalletID.String())
		}
		p.write(w)
	})
}

func principal(r *http.Request) (auth.Principal, error) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return p, nil
}

func pathID(r *http.Request, name string) (uuid.UUID, error) {
	return app.ParseID(name, r.PathValue(name))
}

func operator(r *http.Request) error {
	p, err := principal(r)
	if err != nil {
		return err
	}
	return auth.ManageWallets(p)
}

func walletFromPath(r *http.Request) (uuid.UUID, error) {
	if err := operator(r); err != nil {
		return uuid.Nil, err
	}
	return pathID(r, "walletId")
}

func (a *api) openWallet(w http.ResponseWriter, r *http.Request) error {
	if err := operator(r); err != nil {
		return err
	}
	var body app.OpenWalletRequest
	if err := decode(w, r, &body); err != nil {
		return err
	}
	cmd, err := body.Command(correlationID(r.Context()))
	if err != nil {
		return err
	}
	opened, err := a.wallets.Open(r.Context(), cmd)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/wallets/"+opened.ID.String())
	writeJSON(w, http.StatusCreated, viewWallet(opened))
	return nil
}

func (a *api) getWallet(w http.ResponseWriter, r *http.Request) error {
	id, err := walletFromPath(r)
	if err != nil {
		return err
	}
	s, err := a.wallets.Get(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewWallet(s))
	return nil
}

func (a *api) getLedger(w http.ResponseWriter, r *http.Request) error {
	id, err := walletFromPath(r)
	if err != nil {
		return err
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return invalid("limit must be an integer from 1 to 200", issue{Field: "limit", Code: domain.ErrInvalidValue.Code})
		}
		limit = n
	}
	page, err := a.wallets.Ledger(r.Context(), id, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewLedger(id, page))
	return nil
}

func (a *api) reconcile(w http.ResponseWriter, r *http.Request) error {
	id, err := walletFromPath(r)
	if err != nil {
		return err
	}
	result, err := a.wallets.Reconcile(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewReconciliation(result))
	return nil
}

func (a *api) submitWager(w http.ResponseWriter, r *http.Request) error {
	p, err := principal(r)
	if err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return newProblem(http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", categoryInvalid, "the Idempotency-Key header is required",
			issue{Field: "Idempotency-Key", Code: domain.ErrRequired.Code})
	}
	var body app.WagerRequest
	if err := decode(w, r, &body); err != nil {
		return err
	}
	cmd, err := body.Command(key, correlationID(r.Context()))
	if err != nil {
		return err
	}
	if err := auth.SubmitWager(p, cmd.ProviderID); err != nil {
		return err
	}
	result, err := a.wagers.Submit(r.Context(), cmd)
	if err != nil {
		return err
	}
	status := http.StatusOK
	switch result.Transaction.Status {
	case wager.PendingReference:
		status = http.StatusAccepted
		w.Header().Set("Location", "/wagering/transactions/"+result.Transaction.ID.String())
	case wager.Rejected:
		status = http.StatusUnprocessableEntity
	case wager.Failed:
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, viewResult(result))
	return nil
}

func (a *api) getTransaction(w http.ResponseWriter, r *http.Request) error {
	p, err := principal(r)
	if err != nil {
		return err
	}
	if err := auth.ReadTransactions(p); err != nil {
		return err
	}
	id, err := pathID(r, "transactionId")
	if err != nil {
		return err
	}
	tx, err := a.wagers.Get(r.Context(), id)
	if err != nil {
		return err
	}
	if err := auth.ReadTransaction(p, tx); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewTransaction(tx))
	return nil
}

func (a *api) getProviderTransaction(w http.ResponseWriter, r *http.Request) error {
	p, err := principal(r)
	if err != nil {
		return err
	}
	providerID := r.PathValue("providerId")
	if err := auth.ReadProviderTransaction(p, providerID); err != nil {
		return err
	}
	tx, err := a.wagers.GetByExternalID(r.Context(), providerID, r.PathValue("externalTransactionId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewTransaction(tx))
	return nil
}
