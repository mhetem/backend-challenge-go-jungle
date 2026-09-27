package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"go.uber.org/goleak"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
)

var discard = slog.New(slog.DiscardHandler)

func TestHTTPServerBindsOnStartAndShutsDown(t *testing.T) {
	defer goleak.VerifyNone(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })
	srv := lifecycle.NewHTTPServer("test", &http.Server{Addr: "127.0.0.1:0", Handler: mux}, discard)
	app := fxtest.New(t, fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{OnStart: srv.Start, OnStop: srv.Stop})
	}))
	app.RequireStart()

	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get("http://" + srv.Addr() + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(b) != "pong" {
		t.Fatalf("GET /ping = %q", b)
	}

	app.RequireStop()
	if _, err := client.Get("http://" + srv.Addr() + "/ping"); err == nil {
		t.Fatal("server still answers after stop")
	}
}

func TestHTTPServerFailsStartWhenThePortIsTaken(t *testing.T) {
	first := lifecycle.NewHTTPServer("first", &http.Server{Addr: "127.0.0.1:0"}, discard)
	if err := first.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Stop(context.Background()) }()
	second := lifecycle.NewHTTPServer("second", &http.Server{Addr: first.Addr()}, discard)
	if err := second.Start(t.Context()); err == nil {
		t.Fatalf("second server bound %s too", first.Addr())
	}
	if err := second.Stop(t.Context()); err != nil {
		t.Fatalf("stopping a server that never started: %v", err)
	}
}

func TestWorkersStopWhenCancelled(t *testing.T) {
	defer goleak.VerifyNone(t)
	var running, finished atomic.Int32
	w := lifecycle.NewWorker("loop", 3, func(ctx context.Context) {
		running.Add(1)
		<-ctx.Done()
		finished.Add(1)
	}, discard)
	app := fxtest.New(t, fx.Invoke(func(lc fx.Lifecycle) {
		lc.Append(fx.Hook{OnStart: w.Start, OnStop: w.Stop})
	}))
	app.RequireStart()
	deadline := time.Now().Add(time.Second)
	for running.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	select {
	case <-w.Done():
		t.Fatal("worker reported done while running")
	default:
	}

	app.RequireStop()
	select {
	case <-w.Done():
	default:
		t.Fatal("worker not done after stop")
	}
	if running.Load() != 3 || finished.Load() != 3 {
		t.Fatalf("%d goroutines ran and %d finished; want 3 and 3", running.Load(), finished.Load())
	}
}

func TestWorkerStopHonoursTheDeadline(t *testing.T) {
	release := make(chan struct{})
	w := lifecycle.NewWorker("stuck", 1, func(context.Context) { <-release }, discard)
	if err := w.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := w.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v; want %v", err, context.DeadlineExceeded)
	}
	close(release)
	<-w.Done()
}

func TestWorkerOutlivesTheStartContext(t *testing.T) {
	stopped := make(chan struct{})
	w := lifecycle.NewWorker("long", 1, func(ctx context.Context) {
		<-ctx.Done()
		close(stopped)
	}, discard)
	start, cancel := context.WithCancel(t.Context())
	if err := w.Start(start); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-stopped:
		t.Fatal("worker stopped with its start context")
	case <-time.After(20 * time.Millisecond):
	}
	if err := w.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRetry(t *testing.T) {
	calls := 0
	err := lifecycle.Retry(t.Context(), discard, "flaky", func(context.Context) error {
		calls++
		if calls < 2 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("Retry = %v after %d calls; want success after 2", err, calls)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	down := errors.New("connection refused")
	err = lifecycle.Retry(ctx, discard, "down", func(context.Context) error { return down })
	if !errors.Is(err, down) {
		t.Fatalf("Retry = %v; want it to give up with %v", err, down)
	}
}
