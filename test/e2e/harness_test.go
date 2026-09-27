//go:build e2e

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
	"github.com/mhetem/backend-challenge-go-jungle/test/integration/sqstest"
)

const raceExitCode = 66

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wallet-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "wallet")
	build := exec.Command("go", "build", "-race", "-tags", "failpoints", "-o", binary,
		"github.com/mhetem/backend-challenge-go-jungle/cmd/wallet")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: building the service:", err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type process struct {
	name      string
	cmd       *exec.Cmd
	mu        sync.Mutex
	lines     []string
	httpURL   string
	adminURL  string
	started   chan struct{}
	startOnce sync.Once
	done      chan struct{}
	code      int
	stopped   atomic.Bool
}

func (p *process) consume(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		var entry struct {
			Msg    string `json:"msg"`
			Server string `json:"server"`
			Addr   string `json:"addr"`
		}
		if strings.Contains(line, `"msg":"server listening"`) || strings.Contains(line, `"msg":"wallet service started"`) {
			_ = json.Unmarshal([]byte(line), &entry)
		}
		p.mu.Lock()
		p.lines = append(p.lines, line)
		switch {
		case entry.Msg == "server listening" && entry.Server == "http":
			p.httpURL = "http://" + entry.Addr
		case entry.Msg == "server listening" && entry.Server == "admin":
			p.adminURL = "http://" + entry.Addr
		}
		p.mu.Unlock()
		if entry.Msg == "wallet service started" {
			p.startOnce.Do(func() { close(p.started) })
		}
	}
	_, _ = io.Copy(io.Discard, r)
	_ = p.cmd.Wait()
	p.code = p.cmd.ProcessState.ExitCode()
	close(p.done)
}

func (p *process) urls() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.httpURL, p.adminURL
}

func (p *process) tail(n int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.lines[max(len(p.lines)-n, 0):], "\n")
}

func (p *process) signal(sig syscall.Signal) {
	p.stopped.Store(true)
	_ = p.cmd.Process.Signal(sig)
}

func (p *process) exited(timeout time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

type cluster struct {
	t      *testing.T
	db     *dbtest.Database
	queues *sqstest.Queues
	env    map[string]string
	tokens map[string]string
	http   *http.Client
	procs  []*process
}

func newCluster(t *testing.T, env map[string]string) *cluster {
	t.Helper()
	c := &cluster{
		t:      t,
		db:     dbtest.New(t),
		queues: sqstest.New(t, 10),
		env:    env,
		tokens: map[string]string{},
		http:   &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}},
	}
	for _, client := range []string{"provider-a", "wallet-backoffice"} {
		c.tokens[client] = c.token(client)
	}
	t.Cleanup(c.shutdown)
	return c
}

func (c *cluster) token(client string) string {
	c.t.Helper()
	issuer := os.Getenv("OIDC_ISSUER")
	if issuer == "" {
		issuer = "http://localhost:8080/realms/wagering"
	}
	secret := dbtest.Env(c.t, strings.ToUpper(strings.ReplaceAll(client, "-", "_"))+"_SECRET")
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {secret}}
	resp, err := c.http.PostForm(issuer+"/protocol/openid-connect/token", form)
	if err != nil {
		c.t.Fatalf("token for %s: %v", client, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		c.t.Fatalf("token for %s: %s, %v", client, resp.Status, err)
	}
	return body.AccessToken
}

func (c *cluster) start(name string, extra map[string]string) *process {
	c.t.Helper()
	env := map[string]string{
		"INSTANCE_ID":              "e2e-" + name,
		"COMPONENTS":               "http,consumer,outbox,resolver",
		"FAILPOINTS":               "",
		"LOG_LEVEL":                "info",
		"HTTP_ADDR":                "127.0.0.1:0",
		"ADMIN_ADDR":               "127.0.0.1:0",
		"DATABASE_URL":             c.db.AppURL,
		"DB_MAX_CONNS":             "8",
		"SQS_INPUT_QUEUE":          c.queues.Input,
		"SQS_INPUT_DLQ":            c.queues.DLQ,
		"SQS_EVENTS_QUEUE":         c.queues.Events,
		"SQS_VISIBILITY_TIMEOUT":   "10s",
		"SHUTDOWN_TIMEOUT":         "8s",
		"CONSUMER_MESSAGE_TIMEOUT": "5s",
		"CONSUMER_WAIT_TIME":       "1s",
		"OUTBOX_POLL_INTERVAL":     "100ms",
		"OUTBOX_LEASE":             "3s",
		"RESOLVER_POLL_INTERVAL":   "200ms",
		"PENDING_REF_TTL":          "2m",
		"GORACE":                   "halt_on_error=1",
	}
	maps.Copy(env, c.env)
	maps.Copy(env, extra)
	cmd := exec.Command(binary)
	cmd.Env = os.Environ()
	for _, k := range slices.Sorted(maps.Keys(env)) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		c.t.Fatal(err)
	}
	p := &process{name: name, cmd: cmd, started: make(chan struct{}), done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		c.t.Fatalf("start %s: %v", name, err)
	}
	go p.consume(stderr)
	c.procs = append(c.procs, p)
	select {
	case <-p.started:
	case <-p.done:
		c.t.Fatalf("instance %s exited with %d while starting:\n%s", name, p.code, p.tail(40))
	case <-time.After(45 * time.Second):
		c.t.Fatalf("instance %s did not start in 45s:\n%s", name, p.tail(40))
	}
	eventually(c.t, 15*time.Second, "instance "+name+" to report ready", func() bool { return c.ready(p) == http.StatusOK })
	return p
}

func (c *cluster) shutdown() {
	var running []*process
	for _, p := range c.procs {
		select {
		case <-p.done:
		default:
			p.signal(syscall.SIGTERM)
			running = append(running, p)
		}
	}
	for _, p := range running {
		if !p.exited(30 * time.Second) {
			p.signal(syscall.SIGKILL)
			<-p.done
			c.t.Errorf("instance %s ignored SIGTERM for 30s", p.name)
		}
	}
	for _, p := range c.procs {
		switch {
		case p.code == raceExitCode:
			c.t.Errorf("instance %s: the race detector found a data race (exit %d)", p.name, raceExitCode)
		case !p.stopped.Load():
			c.t.Errorf("instance %s exited on its own with %d", p.name, p.code)
		case slices.Contains(running, p) && p.code != 0:
			c.t.Errorf("instance %s exited with %d after SIGTERM; want 0", p.name, p.code)
		}
	}
	if c.t.Failed() {
		for _, p := range c.procs {
			c.t.Logf("instance %s (exit %d), last log lines:\n%s", p.name, p.code, p.tail(80))
		}
	}
}

func (c *cluster) kill(p *process) {
	c.t.Helper()
	p.signal(syscall.SIGKILL)
	if !p.exited(10 * time.Second) {
		c.t.Fatalf("instance %s survived SIGKILL", p.name)
	}
}

func (c *cluster) terminate(ps ...*process) {
	c.t.Helper()
	for _, p := range ps {
		p.signal(syscall.SIGTERM)
	}
	for _, p := range ps {
		c.expectExit(p, 0, 30*time.Second)
	}
}

func (c *cluster) expectExit(p *process, code int, timeout time.Duration) {
	c.t.Helper()
	p.stopped.Store(true)
	if !p.exited(timeout) {
		c.t.Fatalf("instance %s still running after %s; want exit %d:\n%s", p.name, timeout, code, p.tail(40))
	}
	if p.code != code {
		c.t.Fatalf("instance %s exited with %d; want %d:\n%s", p.name, p.code, code, p.tail(40))
	}
}

type response struct {
	status int
	body   map[string]any
	raw    string
}

func (r response) str(key string) string {
	s, _ := r.body[key].(string)
	return s
}

func (r response) money(key string) string {
	m, _ := r.body[key].(map[string]any)
	amount, _ := m["amount"].(string)
	return amount
}

func (c *cluster) do(p *process, method, path, client string, headers map[string]string, body any) (response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return response{}, err
		}
		reader = bytes.NewReader(b)
	}
	base, _ := p.urls()
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return response{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.tokens[client])
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, err
	}
	r := response{status: resp.StatusCode, raw: string(raw)}
	_ = json.Unmarshal(raw, &r.body)
	return r, nil
}

func (c *cluster) call(p *process, method, path, client string, headers map[string]string, body any, status int) response {
	c.t.Helper()
	r, err := c.do(p, method, path, client, headers, body)
	if err != nil {
		c.t.Fatalf("%s %s on %s: %v", method, path, p.name, err)
	}
	if r.status != status {
		c.t.Fatalf("%s %s on %s = %d %s; want %d", method, path, p.name, r.status, r.raw, status)
	}
	return r
}

func (c *cluster) ready(p *process) int {
	_, admin := p.urls()
	resp, err := c.http.Get(admin + "/health/ready")
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (c *cluster) metric(p *process, series string) float64 {
	c.t.Helper()
	_, admin := p.urls()
	resp, err := c.http.Get(admin + "/metrics")
	if err != nil {
		c.t.Fatalf("metrics of %s: %v", p.name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("metrics of %s: %v", p.name, err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if i := strings.LastIndexByte(line, ' '); i > 0 && line[:i] == series {
			v, _ := strconv.ParseFloat(line[i+1:], 64)
			return v
		}
	}
	return 0
}

func (c *cluster) sum(series string, ps ...*process) float64 {
	c.t.Helper()
	total := 0.0
	for _, p := range ps {
		total += c.metric(p, series)
	}
	return total
}

func consumed(outcome string) string {
	return `wallet_consumer_messages_total{outcome="` + outcome + `"}`
}

type wallet struct {
	id     string
	player string
}

type op struct {
	extID  string
	kind   string
	amount string
	ref    string
}

func (o op) key() string {
	return "provider-a:" + o.extID
}

func (w wallet) wager(o op) map[string]any {
	b := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": o.extID,
		"playerId":              w.player,
		"walletId":              w.id,
		"roundId":               "round-1",
		"gameId":                "game-1",
		"kind":                  o.kind,
		"money":                 map[string]string{"amount": o.amount, "currency": "BRL"},
	}
	if o.ref != "" {
		b["referenceExternalTransactionId"] = o.ref
	}
	return b
}

func (w wallet) envelope(messageID string, o op) string {
	data := w.wager(o)
	data["idempotencyKey"] = o.key()
	b, err := json.Marshal(map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data":       data,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (c *cluster) open(p *process, amount string) wallet {
	c.t.Helper()
	player := uuid.NewString()
	r := c.call(p, http.MethodPost, "/wallets", "wallet-backoffice", nil, map[string]any{
		"playerId":       player,
		"initialBalance": map[string]string{"amount": amount, "currency": "BRL"},
	}, http.StatusCreated)
	return wallet{id: r.str("id"), player: player}
}

func (c *cluster) try(p *process, w wallet, o op) (response, error) {
	return c.do(p, http.MethodPost, "/wagering/transactions", "provider-a", map[string]string{"Idempotency-Key": o.key()}, w.wager(o))
}

func (c *cluster) submit(p *process, w wallet, o op, status int) response {
	c.t.Helper()
	return c.call(p, http.MethodPost, "/wagering/transactions", "provider-a", map[string]string{"Idempotency-Key": o.key()}, w.wager(o), status)
}

func (c *cluster) transaction(p *process, extID string) response {
	c.t.Helper()
	return c.call(p, http.MethodGet, "/providers/provider-a/wagering/transactions/"+extID, "provider-a", nil, nil, http.StatusOK)
}

func (c *cluster) reconcile(p *process, w wallet, balance string) {
	c.t.Helper()
	r := c.call(p, http.MethodPost, "/wallets/"+w.id+"/reconciliation", "wallet-backoffice", nil, nil, http.StatusOK)
	if r.body["consistent"] != true || r.body["continuousVersions"] != true || r.money("storedBalance") != balance ||
		r.money("postedBalance") != balance || r.money("difference") != "0.00" {
		c.t.Fatalf("reconciliation of %s = %s; want consistent at %s", w.id, r.raw, balance)
	}
}

func (c *cluster) send(w wallet, messageID, dedup string, o op) {
	c.t.Helper()
	c.queues.Send(w.envelope(messageID, o), w.id, dedup)
}

func (c *cluster) count(query string, args ...any) int {
	c.t.Helper()
	var n int
	if err := c.db.App.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		c.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (c *cluster) debits(w wallet) int {
	c.t.Helper()
	return c.count(`SELECT count(*) FROM ledger_entries WHERE wallet_id::text = $1 AND direction = 'DEBIT'`, w.id)
}

func (c *cluster) transactions() int {
	c.t.Helper()
	return c.count(`SELECT count(*) FROM wager_transactions WHERE kind <> 'OPENING'`)
}

func (c *cluster) inbox(messageID string) (outcome, transactionID string) {
	c.t.Helper()
	err := c.db.App.QueryRow(context.Background(), `SELECT coalesce(max(outcome), ''), coalesce(max(transaction_id::text), '')
		FROM inbox_messages WHERE message_id = $1`, messageID).Scan(&outcome, &transactionID)
	if err != nil {
		c.t.Fatalf("inbox row %s: %v", messageID, err)
	}
	return outcome, transactionID
}

type outboxRow struct {
	id        string
	attempts  int
	claimedBy string
	published bool
}

func (c *cluster) outbox() []outboxRow {
	c.t.Helper()
	rows, err := c.db.App.Query(context.Background(), `SELECT id::text, attempts, coalesce(claimed_by, ''), published_at IS NOT NULL
		FROM outbox_events ORDER BY seq`)
	if err != nil {
		c.t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.id, &r.attempts, &r.claimedBy, &r.published); err != nil {
			c.t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		c.t.Fatal(err)
	}
	return out
}

type event struct {
	ID          string `json:"eventId"`
	Type        string `json:"eventType"`
	AggregateID string `json:"aggregateId"`
}

func (c *cluster) events(want int) []event {
	c.t.Helper()
	msgs := c.queues.Drain(c.queues.EventsURL, want, 30*time.Second)
	msgs = append(msgs, c.queues.Drain(c.queues.EventsURL, 1, 2*time.Second)...)
	if len(msgs) != want {
		c.t.Fatalf("%d events delivered; want exactly %d", len(msgs), want)
	}
	seen := map[string]bool{}
	out := make([]event, len(msgs))
	for i, m := range msgs {
		if err := json.Unmarshal([]byte(m.Body), &out[i]); err != nil {
			c.t.Fatalf("event body %q: %v", m.Body, err)
		}
		if e := out[i]; e.ID == "" || e.ID != m.DedupID || seen[e.ID] {
			c.t.Fatalf("event %+v arrived with dedup id %q; want its eventId as the dedup id, delivered once", e, m.DedupID)
		}
		seen[out[i].ID] = true
	}
	return out
}

func kinds(evs []event) map[string]int {
	out := map[string]int{}
	for _, e := range evs {
		out[e.Type]++
	}
	return out
}

func (c *cluster) requireQueuesDrained() {
	c.t.Helper()
	eventually(c.t, 30*time.Second, "the input queue to drain", func() bool {
		visible, inFlight := c.queues.Counts(c.queues.InputURL)
		return visible == 0 && inFlight == 0
	})
	if visible, inFlight := c.queues.Counts(c.queues.DLQURL); visible != 0 || inFlight != 0 {
		c.t.Fatalf("the DLQ holds %d messages; want none", visible+inFlight)
	}
}

func (c *cluster) pause(service string) {
	c.t.Helper()
	compose(c.t, "pause", service)
	c.t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "unpause", service)
		cmd.Dir = repoRoot(c.t)
		_ = cmd.Run()
	})
}

func (c *cluster) unpause(service string) {
	c.t.Helper()
	compose(c.t, "unpause", service)
}

func compose(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "compose.yaml")); err != nil {
		t.Fatalf("compose.yaml not found at %s: %v", root, err)
	}
	return root
}

func eventually(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
