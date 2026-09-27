package health

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/fx"
)

const ProbeTimeout = 2 * time.Second

var Module = fx.Module("health",
	fx.Provide(fx.Annotate(NewChecker, fx.ParamTags(`group:"health.checks"`))),
)

type Check struct {
	Name  string
	Probe func(context.Context) error
}

type Checker struct {
	checks   []Check
	log      *slog.Logger
	draining atomic.Bool
}

func NewChecker(checks []Check, log *slog.Logger) *Checker {
	sorted := slices.SortedFunc(slices.Values(checks), func(a, b Check) int { return cmp.Compare(a.Name, b.Name) })
	return &Checker{checks: sorted, log: log}
}

func (c *Checker) Drain() {
	c.draining.Store(true)
}

func (c *Checker) Draining() bool {
	return c.draining.Load()
}

func (c *Checker) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /health/live", c.live)
	mux.HandleFunc("GET /health/ready", c.ready)
}

type report struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

func (c *Checker) live(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, report{Status: "live"})
}

func (c *Checker) ready(w http.ResponseWriter, r *http.Request) {
	if c.Draining() {
		write(w, http.StatusServiceUnavailable, report{Status: "draining"})
		return
	}
	results := make([]error, len(c.checks))
	var wg sync.WaitGroup
	for i, check := range c.checks {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(r.Context(), ProbeTimeout)
			defer cancel()
			results[i] = check.Probe(ctx)
		})
	}
	wg.Wait()
	rep, status := report{Status: "ready", Checks: make(map[string]string, len(c.checks))}, http.StatusOK
	for i, check := range c.checks {
		if err := results[i]; err != nil {
			c.log.WarnContext(r.Context(), "readiness check failed", "check", check.Name, "error", err)
			rep.Checks[check.Name] = "unavailable"
			rep.Status, status = "unavailable", http.StatusServiceUnavailable
			continue
		}
		rep.Checks[check.Name] = "ok"
	}
	write(w, status, rep)
}

func write(w http.ResponseWriter, status int, body report) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
