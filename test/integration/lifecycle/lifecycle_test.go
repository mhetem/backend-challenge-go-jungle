//go:build integration

package lifecycle_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"go.uber.org/goleak"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/httpapi"
	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/sqs"
	"github.com/mhetem/backend-challenge-go-jungle/internal/bootstrap"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/sqstest"
)

func testConfig(t *testing.T, databaseURL string) config.Config {
	t.Helper()
	vars := map[string]string{
		"INSTANCE_ID":  "lifecycle-test",
		"LOG_LEVEL":    "warn",
		"HTTP_ADDR":    "127.0.0.1:0",
		"ADMIN_ADDR":   "127.0.0.1:0",
		"DATABASE_URL": databaseURL,
	}
	cfg, err := config.Parse(func(key string) (string, bool) {
		if v, ok := vars[key]; ok {
			return v, true
		}
		return os.LookupEnv(key)
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

type client struct {
	t    *testing.T
	http *http.Client
}

func (c client) get(url string) (int, string) {
	c.t.Helper()
	resp, err := c.http.Get(url)
	if err != nil {
		c.t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("GET %s body: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

func TestServiceStartsServesAndStopsCleanly(t *testing.T) {
	db := dbtest.New(t)
	cfg := testConfig(t, db.AppURL)
	input := sqstest.New(t, 10)
	cfg.SQS.InputQueue, cfg.SQS.InputDLQ = input.Input, input.DLQ
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var (
		public *httpapi.Server
		admin  *metrics.Admin
		pool   *pgxpool.Pool
		queues *sqs.Client
	)
	service := fxtest.New(t, bootstrap.Options(cfg), fx.Populate(&public, &admin, &pool, &queues))
	service.RequireStart()

	c := client{t: t, http: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
	for _, base := range []string{"http://" + public.Addr(), "http://" + admin.Addr()} {
		if code, body := c.get(base + "/health/live"); code != http.StatusOK {
			t.Fatalf("%s/health/live = %d %s", base, code, body)
		}
		code, body := c.get(base + "/health/ready")
		var ready struct {
			Status string            `json:"status"`
			Checks map[string]string `json:"checks"`
		}
		if err := json.Unmarshal([]byte(body), &ready); err != nil {
			t.Fatalf("%s/health/ready body %q: %v", base, body, err)
		}
		want := map[string]string{"postgres": "ok", "sqs": "ok"}
		if code != http.StatusOK || ready.Status != "ready" || !reflect.DeepEqual(ready.Checks, want) {
			t.Fatalf("%s/health/ready = %d %s; want 200 with both checks ok", base, code, body)
		}
	}
	if q := queues.Queues(); !strings.HasSuffix(q.Input, "/"+cfg.SQS.InputQueue) ||
		!strings.HasSuffix(q.InputDLQ, "/"+cfg.SQS.InputDLQ) || !strings.HasSuffix(q.Events, "/"+cfg.SQS.EventsQueue) {
		t.Fatalf("resolved queue URLs = %+v", q)
	}

	code, exposition := c.get("http://" + admin.Addr() + "/metrics")
	for _, series := range []string{
		"go_goroutines",
		"wallet_reconciliation_divergences_total 0",
		`wallet_http_requests_total{method="GET",route="GET /health/ready",status="200"} 1`,
	} {
		if code != http.StatusOK || !strings.Contains(exposition, series) {
			t.Fatalf("/metrics = %d without %q:\n%s", code, series, exposition)
		}
	}
	if code, _ := c.get("http://" + public.Addr() + "/metrics"); code != http.StatusUnauthorized {
		t.Fatalf("public /metrics = %d; want 401: it belongs on the admin port, and every other public path needs a token", code)
	}

	service.RequireStop()
	if conn, err := pool.Acquire(t.Context()); err == nil {
		conn.Release()
		t.Fatal("pool still hands out connections after stop")
	}
	for _, base := range []string{"http://" + public.Addr(), "http://" + admin.Addr()} {
		if resp, err := c.http.Get(base + "/health/live"); err == nil {
			_ = resp.Body.Close()
			t.Fatalf("%s still answers after stop", base)
		}
	}
}

func TestDisabledHTTPComponentBindsNothing(t *testing.T) {
	db := dbtest.New(t)
	cfg := testConfig(t, db.AppURL)
	cfg.Components = []config.Component{config.Outbox}
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var (
		public *httpapi.Server
		admin  *metrics.Admin
	)
	service := fxtest.New(t, bootstrap.Options(cfg), fx.Populate(&public, &admin))
	service.RequireStart()
	c := client{t: t, http: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
	if public.Addr() != "" {
		t.Fatalf("public server bound %s with the http component disabled", public.Addr())
	}
	if code, body := c.get("http://" + admin.Addr() + "/health/ready"); code != http.StatusOK {
		t.Fatalf("admin readiness = %d %s; want 200", code, body)
	}
	service.RequireStop()
}
