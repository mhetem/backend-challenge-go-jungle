package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
)

func lookup(vars map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}
}

func required() map[string]string {
	return map[string]string{
		"INSTANCE_ID":  "app-1",
		"DATABASE_URL": "postgres://wallet_app:secret@postgres:5432/wallet?sslmode=disable",
		"AWS_REGION":   "us-east-1",
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := config.Parse(lookup(required()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InstanceID != "app-1" || cfg.HTTPAddr != ":8080" || cfg.AdminAddr != ":9090" || cfg.LogLevel != slog.LevelInfo ||
		cfg.StartTimeout != 30*time.Second || cfg.ShutdownTimeout != 20*time.Second {
		t.Fatalf("process defaults = %+v", cfg)
	}
	for _, c := range []config.Component{config.HTTP, config.Consumer, config.Outbox, config.Resolver} {
		if !cfg.Enabled(c) {
			t.Fatalf("component %s is disabled by default", c)
		}
	}
	if db := cfg.Database; db.MaxConns != 16 || db.LockTimeout != 2*time.Second || db.StatementTimeout != 5*time.Second ||
		db.TxAttempts != 3 || db.TxBackoff != 20*time.Millisecond {
		t.Fatalf("database defaults = %+v", db)
	}
	if q := cfg.SQS; q.InputQueue != "wager-transactions.fifo" || q.InputDLQ != "wager-transactions-dlq.fifo" ||
		q.EventsQueue != "wallet-events.fifo" || q.VisibilityTimeout != 30*time.Second || cfg.AWS.EndpointURL != "" {
		t.Fatalf("SQS defaults = %+v, %+v", q, cfg.AWS)
	}
	if o := cfg.OIDC; o.Issuer != "http://localhost:8080/realms/wagering" || o.Audience != "wagering-api" ||
		o.JWKSURL != "http://localhost:8080/realms/wagering/protocol/openid-connect/certs" {
		t.Fatalf("OIDC defaults = %+v", o)
	}
	if o := cfg.Outbox; o.PollInterval != 500*time.Millisecond || o.BatchSize != 50 || o.Lease != 30*time.Second ||
		o.BackoffBase != time.Second || o.BackoffCap != 5*time.Minute {
		t.Fatalf("outbox defaults = %+v", o)
	}
	if r := cfg.Resolver; r.PollInterval != time.Second || r.BatchSize != 100 || r.MaxFailures != 3 {
		t.Fatalf("resolver defaults = %+v", r)
	}
	if c := cfg.Consumer; c.Workers != 4 || c.WaitTime != 20*time.Second || c.MessageTimeout != 10*time.Second {
		t.Fatalf("consumer defaults = %+v", c)
	}
	if p := cfg.PendingReference; p.TTL != 15*time.Minute || p.MaxAttempts != 20 || p.BackoffBase != time.Second || p.BackoffCap != time.Minute {
		t.Fatalf("pending reference defaults = %+v", p)
	}
}

func TestOverrides(t *testing.T) {
	vars := required()
	vars["COMPONENTS"] = " outbox , http "
	vars["LOG_LEVEL"] = "DEBUG"
	vars["HTTP_ADDR"] = "127.0.0.1:0"
	vars["DB_MAX_CONNS"] = "4"
	vars["SHUTDOWN_TIMEOUT"] = "5s"
	vars["CONSUMER_MESSAGE_TIMEOUT"] = "3s"
	vars["CONSUMER_WAIT_TIME"] = "1s"
	vars["AWS_ENDPOINT_URL"] = "http://aws:4566"
	vars["SQS_INPUT_QUEUE"] = "test-input.fifo"
	vars["OIDC_ISSUER"] = "https://idp.example.com/realms/wagering/"
	cfg, err := config.Parse(lookup(vars))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled(config.HTTP) || !cfg.Enabled(config.Outbox) || cfg.Enabled(config.Consumer) || cfg.Enabled(config.Resolver) {
		t.Fatalf("components = %v; want outbox and http", cfg.Components)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.HTTPAddr != "127.0.0.1:0" || cfg.Database.MaxConns != 4 ||
		cfg.ShutdownTimeout != 5*time.Second || cfg.Consumer.MessageTimeout != 3*time.Second || cfg.Consumer.WaitTime != time.Second ||
		cfg.AWS.EndpointURL != "http://aws:4566" || cfg.SQS.InputQueue != "test-input.fifo" ||
		cfg.OIDC.JWKSURL != "https://idp.example.com/realms/wagering/protocol/openid-connect/certs" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		set  map[string]string
		want string
	}{
		{"missing database", map[string]string{"DATABASE_URL": ""}, "DATABASE_URL: is required"},
		{"database without a host", map[string]string{"DATABASE_URL": "postgres:///wallet"}, "DATABASE_URL: must be a postgres or postgresql URL with a host"},
		{"database over http", map[string]string{"DATABASE_URL": "http://postgres:5432/wallet"}, "DATABASE_URL: must be a postgres or postgresql URL with a host"},
		{"missing region", map[string]string{"AWS_REGION": ""}, "AWS_REGION: is required"},
		{"endpoint without a scheme", map[string]string{"AWS_ENDPOINT_URL": "aws:4566"}, "AWS_ENDPOINT_URL: must be a http or https URL with a host"},
		{"unknown component", map[string]string{"COMPONENTS": "http,cron"}, `COMPONENTS: unknown component "cron"`},
		{"trailing comma", map[string]string{"COMPONENTS": "http,"}, `COMPONENTS: unknown component ""`},
		{"repeated component", map[string]string{"COMPONENTS": "http,http"}, `COMPONENTS: component "http" is listed twice`},
		{"unknown level", map[string]string{"LOG_LEVEL": "verbose"}, `LOG_LEVEL: must be debug, info, warn or error, got "verbose"`},
		{"address without a port", map[string]string{"HTTP_ADDR": "localhost"}, `HTTP_ADDR: must be host:port or :port, got "localhost"`},
		{"duration without a unit", map[string]string{"DB_LOCK_TIMEOUT": "2"}, `DB_LOCK_TIMEOUT: must be a positive duration such as 30s, got "2"`},
		{"negative duration", map[string]string{"START_TIMEOUT": "-1s"}, `START_TIMEOUT: must be a positive duration such as 30s, got "-1s"`},
		{"zero connections", map[string]string{"DB_MAX_CONNS": "0"}, `DB_MAX_CONNS: must be a positive integer, got "0"`},
		{"outbox backoff base above its cap", map[string]string{"OUTBOX_BACKOFF_BASE": "10m"}, "OUTBOX_BACKOFF_BASE: must not exceed OUTBOX_BACKOFF_CAP (5m0s)"},
		{"resolver without a batch", map[string]string{"RESOLVER_BATCH_SIZE": "0"}, `RESOLVER_BATCH_SIZE: must be a positive integer, got "0"`},
		{"connections beyond int32", map[string]string{"DB_MAX_CONNS": "3000000000"}, `DB_MAX_CONNS: must be a positive integer, got "3000000000"`},
		{"shutdown outlasting visibility", map[string]string{"SHUTDOWN_TIMEOUT": "30s"}, "SHUTDOWN_TIMEOUT: must be shorter than SQS_VISIBILITY_TIMEOUT (30s)"},
		{"visibility in fractions of a second", map[string]string{"SQS_VISIBILITY_TIMEOUT": "30500ms"}, "SQS_VISIBILITY_TIMEOUT: must be whole seconds up to 12h, got 30.5s"},
		{"visibility beyond twelve hours", map[string]string{"SQS_VISIBILITY_TIMEOUT": "13h"}, "SQS_VISIBILITY_TIMEOUT: must be whole seconds up to 12h, got 13h0m0s"},
		{"long poll beyond twenty seconds", map[string]string{"CONSUMER_WAIT_TIME": "21s"}, "CONSUMER_WAIT_TIME: must be whole seconds from 1s to 20s, got 21s"},
		{"long poll below a second", map[string]string{"CONSUMER_WAIT_TIME": "500ms"}, "CONSUMER_WAIT_TIME: must be whole seconds from 1s to 20s, got 500ms"},
		{"message outlasting shutdown", map[string]string{"CONSUMER_MESSAGE_TIMEOUT": "20s"}, "CONSUMER_MESSAGE_TIMEOUT: must be shorter than SHUTDOWN_TIMEOUT (20s)"},
		{"consumer without workers", map[string]string{"CONSUMER_WORKERS": "0"}, `CONSUMER_WORKERS: must be a positive integer, got "0"`},
		{"backoff base above its cap", map[string]string{"PENDING_REF_BACKOFF_BASE": "2m"}, "PENDING_REF_BACKOFF_BASE: must not exceed PENDING_REF_BACKOFF_CAP (1m0s)"},
		{"issuer without a scheme", map[string]string{"OIDC_ISSUER": "localhost:8080/realms/wagering"}, "OIDC_ISSUER: must be a http or https URL with a host"},
		{"jwks over ftp", map[string]string{"OIDC_JWKS_URL": "ftp://keycloak/certs"}, "OIDC_JWKS_URL: must be a http or https URL with a host"},
		{"empty queue name", map[string]string{"SQS_EVENTS_QUEUE": ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := required()
			for k, v := range tt.set {
				vars[k] = v
			}
			cfg, err := config.Parse(lookup(vars))
			if tt.want == "" {
				if err != nil {
					t.Fatalf("empty %v falls back to the default, got %v", tt.set, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse = %+v, %v; want an error containing %q", cfg, err, tt.want)
			}
		})
	}
}

func TestEveryProblemIsReported(t *testing.T) {
	_, err := config.Parse(lookup(map[string]string{"INSTANCE_ID": "app-1", "DB_TX_ATTEMPTS": "many"}))
	want := "invalid configuration:\nDATABASE_URL: is required\n" +
		"DB_TX_ATTEMPTS: must be a positive integer, got \"many\"\nAWS_REGION: is required"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v; want %q", err, want)
	}
}

func TestLogValueRedactsTheDatabasePassword(t *testing.T) {
	cfg, err := config.Parse(lookup(required()))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	slog.New(slog.NewJSONHandler(&out, nil)).Info("config", "config", cfg)
	if strings.Contains(out.String(), "secret") || !strings.Contains(out.String(), "wallet_app:xxxxx@postgres:5432") {
		t.Fatalf("logged config = %s; want the password redacted", out.String())
	}
}
