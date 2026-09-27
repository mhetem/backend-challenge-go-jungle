package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

const RetryInterval = 500 * time.Millisecond

func Retry(ctx context.Context, log *slog.Logger, what string, fn func(context.Context) error) error {
	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		log.WarnContext(ctx, "dependency not reachable yet", "dependency", what, "error", err)
		timer := time.NewTimer(RetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: gave up waiting: %w", what, err)
		case <-timer.C:
		}
	}
}

type HTTPServer struct {
	name string
	srv  *http.Server
	log  *slog.Logger
	addr string
	done chan struct{}
}

func NewHTTPServer(name string, srv *http.Server, log *slog.Logger) *HTTPServer {
	return &HTTPServer{name: name, srv: srv, log: log}
}

func (s *HTTPServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("%s server: %w", s.name, err)
	}
	s.addr, s.done = ln.Addr().String(), make(chan struct{})
	s.log.InfoContext(ctx, "server listening", "server", s.name, "addr", s.addr)
	go func() {
		defer close(s.done)
		if err := s.srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("server stopped unexpectedly", "server", s.name, "error", err)
		}
	}()
	return nil
}

func (s *HTTPServer) Stop(ctx context.Context) error {
	if s.done == nil {
		return nil
	}
	err := s.srv.Shutdown(ctx)
	<-s.done
	s.log.InfoContext(ctx, "server stopped", "server", s.name)
	return err
}

func (s *HTTPServer) Addr() string {
	return s.addr
}

type Worker struct {
	name   string
	count  int
	run    func(context.Context)
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

func NewWorker(name string, count int, run func(context.Context), log *slog.Logger) *Worker {
	return &Worker{name: name, count: count, run: run, log: log}
}

func (w *Worker) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.cancel, w.done = cancel, make(chan struct{})
	var wg sync.WaitGroup
	for range w.count {
		wg.Go(func() { w.run(runCtx) })
	}
	go func() {
		wg.Wait()
		close(w.done)
	}()
	w.log.InfoContext(ctx, "worker started", "worker", w.name, "goroutines", w.count)
	return nil
}

func (w *Worker) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
		w.log.InfoContext(ctx, "worker stopped", "worker", w.name)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("worker %s did not stop in time: %w", w.name, ctx.Err())
	}
}

func (w *Worker) Done() <-chan struct{} {
	return w.done
}
