package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
)

const (
	categoryInvalid        = "INVALID"
	categoryAuthentication = "AUTHENTICATION"
	categoryAuthorization  = "AUTHORIZATION"
	categoryNotFound       = "NOT_FOUND"
	categoryConflict       = "CONFLICT"
	categoryRejected       = "REJECTED"
	categoryTransient      = "TRANSIENT"
	categoryInternal       = "INTERNAL"
)

type issue struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

type problem struct {
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Status    int     `json:"status"`
	Code      string  `json:"code"`
	Category  string  `json:"category"`
	Detail    string  `json:"detail,omitempty"`
	Retryable bool    `json:"retryable"`
	Errors    []issue `json:"errors"`
}

func (p *problem) Error() string {
	return p.Code + ": " + p.Detail
}

func newProblem(status int, code, category, detail string, issues ...issue) *problem {
	if issues == nil {
		issues = []issue{}
	}
	return &problem{
		Type:      "about:blank",
		Title:     http.StatusText(status),
		Status:    status,
		Code:      code,
		Category:  category,
		Detail:    detail,
		Retryable: status == http.StatusServiceUnavailable,
		Errors:    issues,
	}
}

func invalid(detail string, issues ...issue) *problem {
	return newProblem(http.StatusBadRequest, "INVALID_REQUEST", categoryInvalid, detail, issues...)
}

func (p *problem) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	if p.Retryable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func problemFor(err error) *problem {
	var (
		p      *problem
		exists *app.WalletExistsError
		de     *domain.Error
	)
	switch {
	case errors.As(err, &p):
		return p
	case errors.Is(err, auth.ErrUnauthenticated):
		return newProblem(http.StatusUnauthorized, "UNAUTHENTICATED", categoryAuthentication, "a valid bearer access token is required")
	case errors.Is(err, auth.ErrForbidden):
		return newProblem(http.StatusForbidden, "FORBIDDEN", categoryAuthorization, "the token does not allow this operation")
	case errors.Is(err, app.ErrTransient), errors.Is(err, app.ErrRetryableConflict),
		errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return newProblem(http.StatusServiceUnavailable, "TEMPORARILY_UNAVAILABLE", categoryTransient, "try again shortly")
	case errors.Is(err, app.ErrPermanent):
		return newProblem(http.StatusInternalServerError, "INTERNAL_ERROR", categoryInternal, "the request could not be processed")
	case errors.As(err, &exists):
		return newProblem(http.StatusConflict, domain.ErrWalletExists.Code, categoryConflict,
			"the player already has a wallet in this currency: "+exists.WalletID.String())
	case !errors.As(err, &de):
		return newProblem(http.StatusInternalServerError, "INTERNAL_ERROR", categoryInternal, "the request could not be processed")
	}
	switch de.Category {
	case domain.Invalid:
		return invalid("the request has invalid fields", issues(err)...)
	case domain.NotFound:
		return newProblem(http.StatusNotFound, de.Code, categoryNotFound, "")
	case domain.Conflict:
		return newProblem(http.StatusConflict, de.Code, categoryConflict, "")
	case domain.Rejected:
		return newProblem(http.StatusUnprocessableEntity, de.Code, categoryRejected, "")
	}
	return newProblem(http.StatusInternalServerError, "INTERNAL_ERROR", categoryInternal, "the request could not be processed")
}

func issues(err error) []issue {
	switch e := err.(type) {
	case *domain.FieldError:
		code := domain.ErrInvalidValue.Code
		var de *domain.Error
		if errors.As(e.Err, &de) {
			code = de.Code
		}
		return []issue{{Field: e.Field, Code: code}}
	case interface{ Unwrap() []error }:
		var out []issue
		for _, inner := range e.Unwrap() {
			out = append(out, issues(inner)...)
		}
		return out
	case interface{ Unwrap() error }:
		return issues(e.Unwrap())
	}
	return nil
}
