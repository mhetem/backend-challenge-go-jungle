package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
)

type body struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func get(t *testing.T, c *health.Checker, path string) (int, body) {
	t.Helper()
	mux := http.NewServeMux()
	c.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var b body
	if err := json.NewDecoder(rec.Body).Decode(&b); err != nil {
		t.Fatalf("%s body: %v", path, err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s Content-Type = %q", path, ct)
	}
	return rec.Code, b
}

func ok(context.Context) error { return nil }

func TestLiveIgnoresDependencies(t *testing.T) {
	c := health.NewChecker([]health.Check{{Name: "postgres", Probe: func(context.Context) error { return errors.New("down") }}}, slog.New(slog.DiscardHandler))
	if code, b := get(t, c, "/health/live"); code != http.StatusOK || b.Status != "live" {
		t.Fatalf("live = %d %+v; want 200 live", code, b)
	}
}

func TestReadyReportsEachCheck(t *testing.T) {
	var deadline time.Duration
	checks := []health.Check{
		{Name: "sqs", Probe: func(context.Context) error { return errors.New("connection refused") }},
		{Name: "postgres", Probe: func(ctx context.Context) error {
			d, _ := ctx.Deadline()
			deadline = time.Until(d)
			return nil
		}},
	}
	c := health.NewChecker(checks, slog.New(slog.DiscardHandler))
	code, b := get(t, c, "/health/ready")
	want := body{Status: "unavailable", Checks: map[string]string{"postgres": "ok", "sqs": "unavailable"}}
	if code != http.StatusServiceUnavailable || !reflect.DeepEqual(b, want) {
		t.Fatalf("ready = %d %+v; want 503 %+v", code, b, want)
	}
	if deadline <= 0 || deadline > health.ProbeTimeout {
		t.Fatalf("probe deadline in %s; want within %s", deadline, health.ProbeTimeout)
	}

	c = health.NewChecker([]health.Check{{Name: "postgres", Probe: ok}, {Name: "sqs", Probe: ok}}, slog.New(slog.DiscardHandler))
	code, b = get(t, c, "/health/ready")
	want = body{Status: "ready", Checks: map[string]string{"postgres": "ok", "sqs": "ok"}}
	if code != http.StatusOK || !reflect.DeepEqual(b, want) {
		t.Fatalf("ready = %d %+v; want 200 %+v", code, b, want)
	}

	c.Drain()
	if code, b := get(t, c, "/health/ready"); code != http.StatusServiceUnavailable || b.Status != "draining" || !c.Draining() {
		t.Fatalf("ready while draining = %d %+v; want 503 draining", code, b)
	}
	if code, _ := get(t, c, "/health/live"); code != http.StatusOK {
		t.Fatalf("live while draining = %d; want 200", code)
	}
}
