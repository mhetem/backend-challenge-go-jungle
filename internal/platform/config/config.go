package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Component string

const (
	HTTP     Component = "http"
	Consumer Component = "consumer"
	Outbox   Component = "outbox"
	Resolver Component = "resolver"
)

var knownComponents = []Component{HTTP, Consumer, Outbox, Resolver}

type Config struct {
	InstanceID       string
	Components       []Component
	LogLevel         slog.Level
	HTTPAddr         string
	AdminAddr        string
	StartTimeout     time.Duration
	ShutdownTimeout  time.Duration
	Database         Database
	AWS              AWS
	SQS              SQS
	OIDC             OIDC
	PendingReference PendingReference
	Resolver         ResolverConfig
	Outbox           OutboxConfig
	Consumer         ConsumerConfig
	Tracing          Tracing
}

type Tracing struct {
	Endpoint string
}

type Database struct {
	URL              string
	MaxConns         int32
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	TxAttempts       int
	TxBackoff        time.Duration
}

type AWS struct {
	Region      string
	EndpointURL string
}

type SQS struct {
	InputQueue        string
	InputDLQ          string
	EventsQueue       string
	VisibilityTimeout time.Duration
}

type OIDC struct {
	Issuer   string
	JWKSURL  string
	Audience string
}

type OutboxConfig struct {
	PollInterval time.Duration
	BatchSize    int
	Lease        time.Duration
	BackoffBase  time.Duration
	BackoffCap   time.Duration
}

type ConsumerConfig struct {
	Workers        int
	WaitTime       time.Duration
	MessageTimeout time.Duration
}

type ResolverConfig struct {
	PollInterval time.Duration
	BatchSize    int
	MaxFailures  int
}

type PendingReference struct {
	TTL         time.Duration
	MaxAttempts int
	BackoffBase time.Duration
	BackoffCap  time.Duration
}

func Load() (Config, error) {
	return Parse(os.LookupEnv)
}

func Parse(lookup func(string) (string, bool)) (Config, error) {
	e := env{lookup: lookup}
	cfg := Config{
		InstanceID:      e.text("INSTANCE_ID", hostname()),
		Components:      e.components("COMPONENTS", "http,consumer,outbox,resolver"),
		LogLevel:        e.level("LOG_LEVEL", "info"),
		HTTPAddr:        e.addr("HTTP_ADDR", ":8080"),
		AdminAddr:       e.addr("ADMIN_ADDR", ":9090"),
		StartTimeout:    e.duration("START_TIMEOUT", 30*time.Second),
		ShutdownTimeout: e.duration("SHUTDOWN_TIMEOUT", 20*time.Second),
		Database: Database{
			URL:              e.link("DATABASE_URL", "", "postgres", "postgresql"),
			MaxConns:         int32(e.positive("DB_MAX_CONNS", 16)),
			LockTimeout:      e.duration("DB_LOCK_TIMEOUT", 2*time.Second),
			StatementTimeout: e.duration("DB_STATEMENT_TIMEOUT", 5*time.Second),
			TxAttempts:       e.positive("DB_TX_ATTEMPTS", 3),
			TxBackoff:        e.duration("DB_TX_BACKOFF", 20*time.Millisecond),
		},
		AWS: AWS{
			Region:      e.text("AWS_REGION", ""),
			EndpointURL: e.optionalLink("AWS_ENDPOINT_URL", "http", "https"),
		},
		SQS: SQS{
			InputQueue:        e.text("SQS_INPUT_QUEUE", "wager-transactions.fifo"),
			InputDLQ:          e.text("SQS_INPUT_DLQ", "wager-transactions-dlq.fifo"),
			EventsQueue:       e.text("SQS_EVENTS_QUEUE", "wallet-events.fifo"),
			VisibilityTimeout: e.duration("SQS_VISIBILITY_TIMEOUT", 30*time.Second),
		},
		OIDC: OIDC{
			Issuer:   e.link("OIDC_ISSUER", "http://localhost:8080/realms/wagering", "http", "https"),
			Audience: e.text("OIDC_AUDIENCE", "wagering-api"),
		},
		PendingReference: PendingReference{
			TTL:         e.duration("PENDING_REF_TTL", 15*time.Minute),
			MaxAttempts: e.positive("PENDING_REF_MAX_ATTEMPTS", 20),
			BackoffBase: e.duration("PENDING_REF_BACKOFF_BASE", time.Second),
			BackoffCap:  e.duration("PENDING_REF_BACKOFF_CAP", time.Minute),
		},
		Resolver: ResolverConfig{
			PollInterval: e.duration("RESOLVER_POLL_INTERVAL", time.Second),
			BatchSize:    e.positive("RESOLVER_BATCH_SIZE", 100),
			MaxFailures:  e.positive("RESOLVER_MAX_FAILURES", 3),
		},
		Outbox: OutboxConfig{
			PollInterval: e.duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond),
			BatchSize:    e.positive("OUTBOX_BATCH_SIZE", 50),
			Lease:        e.duration("OUTBOX_LEASE", 30*time.Second),
			BackoffBase:  e.duration("OUTBOX_BACKOFF_BASE", time.Second),
			BackoffCap:   e.duration("OUTBOX_BACKOFF_CAP", 5*time.Minute),
		},
		Consumer: ConsumerConfig{
			Workers:        e.positive("CONSUMER_WORKERS", 4),
			WaitTime:       e.duration("CONSUMER_WAIT_TIME", 20*time.Second),
			MessageTimeout: e.duration("CONSUMER_MESSAGE_TIMEOUT", 10*time.Second),
		},
		Tracing: Tracing{
			Endpoint: strings.TrimSuffix(e.optionalLink("OTEL_EXPORTER_OTLP_ENDPOINT", "http", "https"), "/"),
		},
	}
	cfg.OIDC.JWKSURL = e.link("OIDC_JWKS_URL", strings.TrimSuffix(cfg.OIDC.Issuer, "/")+"/protocol/openid-connect/certs", "http", "https")
	if v := cfg.SQS.VisibilityTimeout; v%time.Second != 0 || v > 12*time.Hour {
		e.fail("SQS_VISIBILITY_TIMEOUT", "must be whole seconds up to 12h, got %s", v)
	}
	if cfg.ShutdownTimeout >= cfg.SQS.VisibilityTimeout {
		e.fail("SHUTDOWN_TIMEOUT", "must be shorter than SQS_VISIBILITY_TIMEOUT (%s)", cfg.SQS.VisibilityTimeout)
	}
	if w := cfg.Consumer.WaitTime; w%time.Second != 0 || w > 20*time.Second {
		e.fail("CONSUMER_WAIT_TIME", "must be whole seconds from 1s to 20s, got %s", w)
	}
	if cfg.Consumer.MessageTimeout >= cfg.ShutdownTimeout {
		e.fail("CONSUMER_MESSAGE_TIMEOUT", "must be shorter than SHUTDOWN_TIMEOUT (%s)", cfg.ShutdownTimeout)
	}
	if cfg.PendingReference.BackoffBase > cfg.PendingReference.BackoffCap {
		e.fail("PENDING_REF_BACKOFF_BASE", "must not exceed PENDING_REF_BACKOFF_CAP (%s)", cfg.PendingReference.BackoffCap)
	}
	if cfg.Outbox.BackoffBase > cfg.Outbox.BackoffCap {
		e.fail("OUTBOX_BACKOFF_BASE", "must not exceed OUTBOX_BACKOFF_CAP (%s)", cfg.Outbox.BackoffCap)
	}
	if err := errors.Join(e.errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

func (c Config) Enabled(component Component) bool {
	return slices.Contains(c.Components, component)
}

func (c Config) LogValue() slog.Value {
	names := make([]string, len(c.Components))
	for i, component := range c.Components {
		names[i] = string(component)
	}
	return slog.GroupValue(
		slog.String("instanceId", c.InstanceID),
		slog.String("components", strings.Join(names, ",")),
		slog.String("httpAddr", c.HTTPAddr),
		slog.String("adminAddr", c.AdminAddr),
		slog.Duration("shutdownTimeout", c.ShutdownTimeout),
		slog.String("databaseUrl", redacted(c.Database.URL)),
		slog.Int("databaseMaxConns", int(c.Database.MaxConns)),
		slog.String("awsRegion", c.AWS.Region),
		slog.String("awsEndpointUrl", c.AWS.EndpointURL),
		slog.String("inputQueue", c.SQS.InputQueue),
		slog.String("eventsQueue", c.SQS.EventsQueue),
		slog.String("oidcIssuer", c.OIDC.Issuer),
		slog.String("oidcJwksUrl", c.OIDC.JWKSURL),
		slog.String("otlpEndpoint", c.Tracing.Endpoint),
	)
}

func redacted(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Redacted()
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

type env struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (e *env) fail(key, format string, args ...any) {
	e.errs = append(e.errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
}

func (e *env) value(key, fallback string) (string, bool) {
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return fallback, false
	}
	return v, true
}

func (e *env) text(key, fallback string) string {
	v, _ := e.value(key, fallback)
	if v == "" {
		e.fail(key, "is required")
	}
	return v
}

func (e *env) duration(key string, fallback time.Duration) time.Duration {
	raw, ok := e.value(key, "")
	if !ok {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		e.fail(key, "must be a positive duration such as 30s, got %q", raw)
		return fallback
	}
	return d
}

func (e *env) positive(key string, fallback int) int {
	raw, ok := e.value(key, "")
	if !ok {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n <= 0 {
		e.fail(key, "must be a positive integer, got %q", raw)
		return fallback
	}
	return int(n)
}

func (e *env) level(key, fallback string) slog.Level {
	raw, _ := e.value(key, fallback)
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		e.fail(key, "must be debug, info, warn or error, got %q", raw)
	}
	return level
}

func (e *env) addr(key, fallback string) string {
	raw, _ := e.value(key, fallback)
	if _, port, err := net.SplitHostPort(raw); err != nil || port == "" {
		e.fail(key, "must be host:port or :port, got %q", raw)
	}
	return raw
}

func (e *env) link(key, fallback string, schemes ...string) string {
	raw := e.text(key, fallback)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err != nil || !slices.Contains(schemes, u.Scheme) || u.Host == "" {
		e.fail(key, "must be a %s URL with a host", strings.Join(schemes, " or "))
	}
	return raw
}

func (e *env) optionalLink(key string, schemes ...string) string {
	if _, ok := e.value(key, ""); !ok {
		return ""
	}
	return e.link(key, "", schemes...)
}

func (e *env) components(key, fallback string) []Component {
	raw, _ := e.value(key, fallback)
	var out []Component
	for part := range strings.SplitSeq(raw, ",") {
		c := Component(strings.TrimSpace(part))
		switch {
		case !slices.Contains(knownComponents, c):
			e.fail(key, "unknown component %q (known: http, consumer, outbox, resolver)", c)
		case slices.Contains(out, c):
			e.fail(key, "component %q is listed twice", c)
		default:
			out = append(out, c)
		}
	}
	return out
}
