package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
)

const (
	maxBodyBytes      = 64 << 10
	requestTimeout    = 10 * time.Second
	correlationHeader = "X-Correlation-Id"
)

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		return newProblem(http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", categoryInvalid, "the body must be application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		if dec.Decode(&struct{}{}) != io.EOF {
			return invalid("the body must hold exactly one JSON object")
		}
		return nil
	}
	var (
		tooLarge  *http.MaxBytesError
		typeError *json.UnmarshalTypeError
		syntax    *json.SyntaxError
	)
	switch {
	case errors.As(err, &tooLarge):
		return newProblem(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", categoryInvalid, "the body is limited to 64 KiB")
	case errors.As(err, &typeError):
		return invalid("a field has the wrong JSON type", issue{Field: typeError.Field, Code: domain.ErrInvalidValue.Code})
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return invalid("the body has a field the contract does not define", issue{Field: field, Code: "UNKNOWN_FIELD"})
	case errors.Is(err, io.EOF):
		return invalid("the body is empty")
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		return invalid("the body is not valid JSON")
	}
	return invalid("the body could not be read")
}

type correlationKey struct{}

func correlationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

func correlate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(correlationHeader)
		valid := id == "" || app.ValidToken(id)
		if id == "" || !valid {
			id = app.NewID().String()
		}
		w.Header().Set(correlationHeader, id)
		if !valid {
			invalid("the correlation id must be 1-128 visible ASCII characters",
				issue{Field: correlationHeader, Code: domain.ErrInvalidValue.Code}).write(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		ctx = context.WithValue(ctx, correlationKey{}, id)
		ctx = logging.With(ctx, slog.String("correlationId", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
