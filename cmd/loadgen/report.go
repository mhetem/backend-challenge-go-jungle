package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type backlog struct {
	sampled bool
	events  float64
	age     float64
}

func sampleBacklog(db *pgxpool.Pool, a *api, admins []string, withMetrics bool) func() backlog {
	done, result := make(chan struct{}), make(chan backlog, 1)
	go func() {
		b := backlog{sampled: db != nil || withMetrics}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				result <- b
				return
			case <-ticker.C:
				if events, age, ok := pendingNow(db, a, admins, withMetrics); ok {
					b.events, b.age = max(b.events, events), max(b.age, age)
				}
			}
		}
	}()
	return func() backlog {
		close(done)
		return <-result
	}
}

func pendingNow(db *pgxpool.Pool, a *api, admins []string, withMetrics bool) (events, age float64, ok bool) {
	ctx := context.Background()
	if db != nil {
		err := db.QueryRow(ctx, `SELECT count(*)::float8, coalesce(extract(epoch FROM now() - min(occurred_at)), 0)::float8
			FROM outbox_events WHERE published_at IS NULL`).Scan(&events, &age)
		return events, age, err == nil
	}
	if !withMetrics {
		return 0, 0, false
	}
	instances, err := a.scrapeAll(ctx, admins)
	if err != nil {
		return 0, 0, false
	}
	return peak(instances, "wallet_outbox_pending"), peak(instances, "wallet_outbox_oldest_pending_age_seconds"), true
}

type drainResult struct {
	known   bool
	drained bool
	left    float64
	waited  time.Duration
}

func waitDrain(ctx context.Context, db *pgxpool.Pool, a *api, admins []string, limit time.Duration) drainResult {
	start := time.Now()
	for {
		pending, known := outstanding(ctx, db, a, admins)
		switch {
		case !known:
			return drainResult{}
		case pending == 0:
			return drainResult{known: true, drained: true, waited: time.Since(start)}
		case time.Since(start) >= limit:
			return drainResult{known: true, left: pending, waited: time.Since(start)}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func outstanding(ctx context.Context, db *pgxpool.Pool, a *api, admins []string) (float64, bool) {
	if db != nil {
		var n int64
		if err := db.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&n); err == nil {
			return float64(n), true
		}
	}
	instances, err := a.scrapeAll(ctx, admins)
	if err != nil {
		return 0, false
	}
	return peak(instances, "wallet_outbox_pending"), true
}

type outboxLag struct {
	events             int64
	p50, p95, p99, top float64
}

func measureLag(ctx context.Context, db *pgxpool.Pool, since time.Time) (outboxLag, error) {
	var l outboxLag
	err := db.QueryRow(ctx, `SELECT count(*),
		coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY lag), 0),
		coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY lag), 0),
		coalesce(percentile_cont(0.99) WITHIN GROUP (ORDER BY lag), 0),
		coalesce(max(lag), 0)
		FROM (SELECT extract(epoch FROM published_at - occurred_at)::float8 AS lag
			FROM outbox_events WHERE occurred_at >= $1 AND published_at IS NOT NULL) AS published`, since).
		Scan(&l.events, &l.p50, &l.p95, &l.p99, &l.top)
	return l, err
}

type environment struct {
	platform  string
	cpu       string
	cores     int
	memory    string
	goVersion string
}

func describe() environment {
	e := environment{
		platform:  runtime.GOOS + "/" + runtime.GOARCH,
		cpu:       fallback(procField("/proc/cpuinfo", "model name"), "unknown CPU"),
		cores:     runtime.NumCPU(),
		memory:    "unknown",
		goVersion: runtime.Version(),
	}
	if kb, err := strconv.ParseFloat(strings.TrimSuffix(procField("/proc/meminfo", "MemTotal"), " kB"), 64); err == nil {
		e.memory = fmt.Sprintf("%.1f GiB", kb/(1<<20))
	}
	return e
}

func procField(path, key string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(raw)) {
		if name, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) == key {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type latency struct {
	label              string
	count              int
	p50, p95, p99, top time.Duration
}

func summarize(samples []sample) []latency {
	groups := map[string][]time.Duration{}
	all := make([]time.Duration, 0, len(samples))
	for _, s := range samples {
		groups[s.label] = append(groups[s.label], s.latency)
		all = append(all, s.latency)
	}
	out := []latency{measure("all", all)}
	for _, label := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK", "REPLAY"} {
		if d, ok := groups[label]; ok {
			out = append(out, measure(label, d))
		}
	}
	return out
}

func measure(label string, d []time.Duration) latency {
	slices.Sort(d)
	return latency{label: label, count: len(d), p50: percentile(d, 0.50), p95: percentile(d, 0.95), p99: percentile(d, 0.99), top: percentile(d, 1)}
}

func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

type outcome struct {
	name  string
	count int
}

func tally(samples []sample) (outcomes []outcome, failures, conflicts int) {
	counts := map[string]int{}
	for _, s := range samples {
		counts[s.outcome]++
		switch {
		case failed(s.outcome):
			failures++
		case strings.HasPrefix(s.outcome, "conflict:"):
			conflicts++
		}
	}
	for name, count := range counts {
		outcomes = append(outcomes, outcome{name, count})
	}
	slices.SortFunc(outcomes, func(a, b outcome) int {
		return cmp.Or(cmp.Compare(b.count, a.count), cmp.Compare(a.name, b.name))
	})
	return outcomes, failures, conflicts
}

type report struct {
	env        environment
	o          options
	res        result
	metrics    bool
	before     []map[string]float64
	after      []map[string]float64
	backlog    backlog
	drain      drainResult
	lag        outboxLag
	lagKnown   bool
	wallets    int
	consistent int
}

func (r report) delta(series string) string {
	if !r.metrics {
		return "n/a"
	}
	return strconv.FormatFloat(total(r.after, series)-total(r.before, series), 'f', -1, 64)
}

func (r report) write(w io.Writer) {
	p := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
	}
	requests := len(r.res.samples)
	outcomes, errs, conflicts := tally(r.res.samples)
	errorRate := 0.0
	if requests > 0 {
		errorRate = 100 * float64(errs) / float64(requests)
	}

	p("# Load test")
	p("")
	p("Environment:")
	p("- load generator host: %s, %s, %d logical CPUs, %s RAM", r.env.platform, r.env.cpu, r.env.cores, r.env.memory)
	p("- loadgen built with %s; targets %s", r.env.goVersion, strings.Join(r.o.targets, ", "))
	p("- started %s", r.res.start.UTC().Format(time.RFC3339))
	p("")
	p("Workload:")
	workload := fmt.Sprintf("%s for %s", mode(r.o), r.o.duration)
	if r.o.rate > 0 {
		workload += fmt.Sprintf(", at most %d requests in flight", r.o.concurrency)
	}
	p("- %s", workload)
	p("- %d wallets; %.0f%% of new operations on one hot wallet; %.0f%% of requests replay an earlier operation", r.o.wallets, 100*r.o.hot, 100*r.o.replays)
	p("- new operations: BET 60%%, WIN 25%%, LOSS 5%%, REFUND 5%%, ROLLBACK 5%% (reversals of the worker's own processed operations, a BET when none is left)")
	p("")
	p("| Requests | Elapsed | Throughput | Errors |")
	p("|---:|---:|---:|---:|")
	p("| %d | %.1f s | %.1f req/s | %d (%.2f%%) |", requests, r.res.elapsed.Seconds(), float64(requests)/r.res.elapsed.Seconds(), errs, errorRate)
	p("")
	if r.o.rate > 0 {
		p("Latency in ms, measured by the client from each request's scheduled start, so queueing behind a slow service counts:")
	} else {
		p("Latency in ms, measured by the client from the moment each request is sent:")
	}
	p("")
	p("| Operation | Requests | p50 | p95 | p99 | Max |")
	p("|---|---:|---:|---:|---:|---:|")
	for _, l := range summarize(r.res.samples) {
		p("| %s | %d | %s | %s | %s | %s |", l.label, l.count, ms(l.p50), ms(l.p95), ms(l.p99), ms(l.top))
	}
	p("")
	p("| Outcome | Requests |")
	p("|---|---:|")
	for _, o := range outcomes {
		p("| %s | %d |", o.name, o.count)
	}
	p("")
	p("Conflicts:")
	p("")
	p("| Signal | Count |")
	p("|---|---:|")
	for _, reason := range []string{"serialization", "deadlock", "lock_timeout", "conflict"} {
		p("| Transactions rerun by `InTx` (%s) | %s |", reason, r.delta(`wallet_db_transaction_retries_total{reason="`+reason+`"}`))
	}
	p("| HTTP 409 responses | %d |", conflicts)
	p("| Outbox claims lost to another instance | %s |", r.delta(`wallet_outbox_publish_results_total{result="lost"}`))
	p("")
	p("Outbox:")
	p("")
	p("| Measure | Value |")
	p("|---|---:|")
	p("| Events published during the run and the drain | %s |", r.delta(`wallet_outbox_publish_results_total{result="published"}`))
	if r.backlog.sampled {
		p("| Largest backlog seen | %.0f events |", r.backlog.events)
		p("| Oldest pending event, worst seen | %.1f s |", r.backlog.age)
	}
	if r.lagKnown {
		p("| Commit to publish, p50 / p95 / p99 / max | %.2f / %.2f / %.2f / %.2f s over %d events |", r.lag.p50, r.lag.p95, r.lag.p99, r.lag.top, r.lag.events)
	}
	switch {
	case !r.drain.known:
		p("| Drain after the load | not measured |")
	case r.drain.drained:
		p("| Drain after the load | %.1f s |", r.drain.waited.Seconds())
	default:
		p("| Drain after the load | %.0f events left after %s |", r.drain.left, r.o.drain)
	}
	p("")
	p("Reconciliation: %d of %d wallets consistent.", r.consistent, r.wallets)
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64)
}
