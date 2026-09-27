package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type options struct {
	targets     []string
	admins      []string
	issuer      string
	databaseURL string
	duration    time.Duration
	concurrency int
	rate        int
	wallets     int
	hot         float64
	replays     float64
	drain       time.Duration
}

func main() {
	opts, err := parse(os.Args[1:], os.Getenv)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return
	case err != nil:
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	if err := run(ctx, opts, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func parse(args []string, getenv func(string) string) (options, error) {
	fs := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	targets := fs.String("targets", "http://localhost:8081,http://localhost:8082,http://localhost:8083", "API base URLs, comma-separated")
	admins := fs.String("admin", "http://localhost:9091,http://localhost:9092,http://localhost:9093", "admin base URLs for /metrics, comma-separated; empty skips metrics")
	issuer := fs.String("issuer", fallback(getenv("OIDC_ISSUER"), "http://localhost:8080/realms/wagering"), "Keycloak realm URL")
	database := fs.String("database-url", getenv("DATABASE_URL"), "PostgreSQL URL for the outbox lag; empty skips it")
	var o options
	fs.DurationVar(&o.duration, "duration", time.Minute, "how long to generate load")
	fs.IntVar(&o.concurrency, "concurrency", 32, "most requests in flight")
	fs.IntVar(&o.rate, "rate", 100, "requests per second in an open loop; 0 runs a closed loop at full concurrency")
	fs.IntVar(&o.wallets, "wallets", 50, "wallets the load is spread over")
	fs.Float64Var(&o.hot, "hot", 0, "share of new operations sent to one hot wallet")
	fs.Float64Var(&o.replays, "replays", 0.05, "share of requests that resend an earlier operation")
	fs.DurationVar(&o.drain, "drain", 2*time.Minute, "how long to wait for the outbox to drain after the load")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	o.targets, o.admins, o.issuer, o.databaseURL = list(*targets), list(*admins), strings.TrimSuffix(*issuer, "/"), *database
	var problems []error
	check := func(ok bool, problem string) {
		if !ok {
			problems = append(problems, errors.New(problem))
		}
	}
	check(len(o.targets) > 0, "-targets: at least one URL is required")
	check(o.duration > 0, "-duration: must be positive")
	check(o.concurrency > 0, "-concurrency: must be positive")
	check(o.rate >= 0 && o.rate <= 100000, "-rate: must be from 0 to 100000")
	check(o.wallets > 0, "-wallets: must be positive")
	check(o.hot >= 0 && o.hot <= 1, "-hot: must be from 0 to 1")
	check(o.replays >= 0 && o.replays < 1, "-replays: must be at least 0 and below 1")
	check(o.drain > 0, "-drain: must be positive")
	return o, errors.Join(problems...)
}

func run(ctx context.Context, o options, out, progress io.Writer) error {
	a := newAPI(o.issuer, o.concurrency)
	for _, client := range []string{providerClient, operatorClient} {
		if _, err := a.tokens.get(ctx, client); err != nil {
			return err
		}
	}
	var db *pgxpool.Pool
	if o.databaseURL != "" {
		pool, err := pgxpool.New(ctx, o.databaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		db = pool
	}

	fmt.Fprintf(progress, "loadgen: opening %d wallets\n", o.wallets)
	wallets, err := openWallets(ctx, a, o.targets, o.wallets)
	if err != nil {
		return err
	}
	before, err := a.scrapeAll(ctx, o.admins)
	withMetrics := err == nil
	if !withMetrics {
		fmt.Fprintf(progress, "loadgen: metrics unavailable, conflicts and backlog are skipped: %v\n", err)
	}

	fmt.Fprintf(progress, "loadgen: %s for %s\n", mode(o), o.duration)
	stopSampling := sampleBacklog(db, a, o.admins, withMetrics)
	res := generate(ctx, o, a, wallets, runID(), progress)
	backlog := stopSampling()

	post := context.WithoutCancel(ctx)
	fmt.Fprintln(progress, "loadgen: waiting for the outbox to drain")
	drain := waitDrain(post, db, a, o.admins, o.drain)
	var after []map[string]float64
	if withMetrics {
		if after, err = a.scrapeAll(post, o.admins); err != nil {
			withMetrics = false
		}
	}
	var lag outboxLag
	lagKnown := false
	if db != nil {
		if lag, err = measureLag(post, db, res.start); err != nil {
			fmt.Fprintf(progress, "loadgen: measuring the outbox lag failed: %v\n", err)
		} else {
			lagKnown = true
		}
	}
	fmt.Fprintln(progress, "loadgen: reconciling every wallet")
	consistent, err := reconcileAll(post, a, o.targets, wallets)
	if err != nil {
		return err
	}

	report{
		env:        describe(),
		o:          o,
		res:        res,
		metrics:    withMetrics,
		before:     before,
		after:      after,
		backlog:    backlog,
		drain:      drain,
		lag:        lag,
		lagKnown:   lagKnown,
		wallets:    len(wallets),
		consistent: consistent,
	}.write(out)
	if consistent < len(wallets) {
		return fmt.Errorf("%d of %d wallets failed reconciliation", len(wallets)-consistent, len(wallets))
	}
	return nil
}

func mode(o options) string {
	if o.rate > 0 {
		return fmt.Sprintf("open loop at %d requests/s", o.rate)
	}
	return fmt.Sprintf("closed loop with %d requests in flight", o.concurrency)
}

func runID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func list(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSuffix(strings.TrimSpace(part), "/"); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func fallback(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
