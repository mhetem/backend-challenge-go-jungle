package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
)

func decode(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestContextAttributesAreLogged(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo).With("instanceId", "app-1")
	ctx := logging.With(context.Background(), slog.String("correlationId", "corr-1"))
	wallet := logging.With(ctx, slog.String("walletId", "w-1"))
	message := logging.With(ctx, slog.String("messageId", "m-1"))

	log.InfoContext(wallet, "debited")
	log.InfoContext(message, "received")
	log.Info("without context")
	log.DebugContext(wallet, "filtered out")

	lines := decode(t, &buf)
	if len(lines) != 3 {
		t.Fatalf("%d lines; want 3 (debug is below the level): %v", len(lines), lines)
	}
	want := []map[string]any{
		{"msg": "debited", "instanceId": "app-1", "correlationId": "corr-1", "walletId": "w-1"},
		{"msg": "received", "instanceId": "app-1", "correlationId": "corr-1", "messageId": "m-1"},
		{"msg": "without context", "instanceId": "app-1"},
	}
	for i, fields := range want {
		for k, v := range fields {
			if lines[i][k] != v {
				t.Fatalf("line %d %s = %v; want %v (%v)", i, k, lines[i][k], v, lines[i])
			}
		}
	}
	if _, leaked := lines[1]["walletId"]; leaked {
		t.Fatalf("sibling context leaked walletId into %v", lines[1])
	}
	if _, leaked := lines[2]["correlationId"]; leaked {
		t.Fatalf("background context carried %v", lines[2])
	}
}

func TestSensitiveKeysAreRedacted(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo)
	log.Info("request", "Authorization", "Bearer abc", "clientSecret", "s3cr3t", "accessToken", "t0k3n", "walletId", "w-1")
	line := decode(t, &buf)[0]
	for _, key := range []string{"Authorization", "clientSecret", "accessToken"} {
		if line[key] != "[REDACTED]" {
			t.Fatalf("%s = %v; want [REDACTED]", key, line[key])
		}
	}
	if line["walletId"] != "w-1" {
		t.Fatalf("walletId = %v; want it kept", line["walletId"])
	}
}
