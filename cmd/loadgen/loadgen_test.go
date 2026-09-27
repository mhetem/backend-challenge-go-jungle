package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://wallet_app@localhost:55432/wallet"}
	o, err := parse(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want := options{
		targets:     []string{"http://localhost:8081", "http://localhost:8082", "http://localhost:8083"},
		admins:      []string{"http://localhost:9091", "http://localhost:9092", "http://localhost:9093"},
		issuer:      "http://localhost:8080/realms/wagering",
		databaseURL: "postgres://wallet_app@localhost:55432/wallet",
		duration:    time.Minute,
		concurrency: 32,
		rate:        100,
		wallets:     50,
		replays:     0.05,
		drain:       2 * time.Minute,
	}
	if !reflect.DeepEqual(o, want) {
		t.Fatalf("defaults = %+v; want %+v", o, want)
	}
}

func TestParseRejectsBadValues(t *testing.T) {
	_, err := parse([]string{"-targets", " , ", "-rate", "-1", "-hot", "1.5", "-replays", "1", "-concurrency", "0"}, func(string) string { return "" })
	for _, want := range []string{"-targets", "-rate", "-hot", "-replays", "-concurrency"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v; want it to mention %s", err, want)
		}
	}
	o, err := parse([]string{"-targets", "http://a:1/, http://b:2", "-admin", "", "-issuer", "http://kc/realms/x/", "-rate", "0"}, func(string) string { return "" })
	if err != nil || !reflect.DeepEqual(o.targets, []string{"http://a:1", "http://b:2"}) || o.admins != nil || o.issuer != "http://kc/realms/x" || o.rate != 0 {
		t.Fatalf("parse = %+v, %v", o, err)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		r    reply
		err  error
		want string
	}{
		{reply{}, errors.New("connection refused"), "transport_error"},
		{reply{status: 200}, nil, "processed"},
		{reply{status: 200, replay: true}, nil, "replayed"},
		{reply{status: 422, replay: true, failure: "INSUFFICIENT_FUNDS"}, nil, "replayed"},
		{reply{status: 202}, nil, "pending_reference"},
		{reply{status: 422, failure: "INSUFFICIENT_FUNDS"}, nil, "rejected:INSUFFICIENT_FUNDS"},
		{reply{status: 409, code: "IDEMPOTENCY_KEY_REUSED"}, nil, "conflict:IDEMPOTENCY_KEY_REUSED"},
		{reply{status: 503}, nil, "unavailable"},
		{reply{status: 500}, nil, "http_500"},
	}
	for _, tt := range tests {
		if got := classify(tt.r, tt.err); got != tt.want {
			t.Errorf("classify(%+v, %v) = %s; want %s", tt.r, tt.err, got, tt.want)
		}
	}
	for outcome, want := range map[string]bool{"transport_error": true, "unavailable": true, "http_500": true, "processed": false, "rejected:X": false, "conflict:X": false} {
		if failed(outcome) != want {
			t.Errorf("failed(%s) = %t; want %t", outcome, !want, want)
		}
	}
}

func TestParseMetrics(t *testing.T) {
	text := `# HELP wallet_http_requests_total HTTP requests.
# TYPE wallet_http_requests_total counter
wallet_http_requests_total{method="POST",route="POST /wagering/transactions",status="200"} 42
wallet_outbox_pending 7
wallet_db_transaction_retries_total{reason="lock_timeout"} 3
broken line
`
	got := parseMetrics(text)
	if len(got) != 3 || got[`wallet_http_requests_total{method="POST",route="POST /wagering/transactions",status="200"}`] != 42 ||
		got["wallet_outbox_pending"] != 7 || got[`wallet_db_transaction_retries_total{reason="lock_timeout"}`] != 3 {
		t.Fatalf("parseMetrics = %v", got)
	}
	instances := []map[string]float64{{"a": 1, "b": 5}, {"a": 2, "b": 3}}
	if total(instances, "a") != 3 || peak(instances, "b") != 5 || total(instances, "missing") != 0 {
		t.Fatalf("total/peak over %v", instances)
	}
}

func TestPercentile(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	for q, want := range map[float64]time.Duration{0.5: 50 * time.Millisecond, 0.95: 95 * time.Millisecond, 0.99: 99 * time.Millisecond, 1: 100 * time.Millisecond} {
		if got := percentile(d, q); got != want {
			t.Errorf("percentile(%v) = %s; want %s", q, got, want)
		}
	}
	if percentile(nil, 0.5) != 0 || percentile([]time.Duration{time.Second}, 0.99) != time.Second {
		t.Fatal("percentile of an empty or single-sample set")
	}
}

func TestAmount(t *testing.T) {
	for minor, want := range map[int64]string{0: "0.00", 5: "0.05", 100: "1.00", 5000: "50.00", 123456: "1234.56"} {
		if got := amount(minor); got != want {
			t.Errorf("amount(%d) = %s; want %s", minor, got, want)
		}
	}
}

func TestPlanReversesEachProcessedOperationOnce(t *testing.T) {
	w := &worker{id: 3, run: "test", o: options{replays: 0.1, hot: 0.2},
		wallets: []wallet{{"w-1", "p-1"}, {"w-2", "p-2"}, {"w-3", "p-3"}}, rng: rand.New(rand.NewPCG(1, 2))}
	sent := map[string]operation{}
	reversed := map[string]bool{}
	kinds := map[string]int{}
	replays := 0
	for range 5000 {
		op, replay := w.plan()
		if replay {
			if prior, ok := sent[op.extID]; !ok || prior != op {
				t.Fatalf("replay %+v; want an identical copy of an operation already sent", op)
			}
			replays++
			continue
		}
		if _, dup := sent[op.extID]; dup {
			t.Fatalf("external id %s used twice", op.extID)
		}
		kinds[op.kind]++
		if (op.kind == "LOSS") != (op.minor == 0) || op.minor < 0 || op.minor > 5000 {
			t.Fatalf("%s for %s; want 0.00 only for LOSS and at most 50.00", op.kind, amount(op.minor))
		}
		if op.ref != "" {
			ref, ok := sent[op.ref]
			switch {
			case !ok || reversed[op.ref]:
				t.Fatalf("%s reverses %s, which was not sent or is already reversed", op.extID, op.ref)
			case op.kind == "REFUND" && ref.kind != "BET", op.kind == "ROLLBACK" && ref.kind != "BET" && ref.kind != "WIN":
				t.Fatalf("%s of a %s", op.kind, ref.kind)
			case op.wallet != ref.wallet || op.round != ref.round || op.minor != ref.minor:
				t.Fatalf("%+v reverses %+v with another wallet, round or amount", op, ref)
			}
			reversed[op.ref] = true
		}
		w.remember(op, "processed")
		sent[op.extID] = op
	}
	for _, kind := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		if kinds[kind] == 0 {
			t.Errorf("no %s in 5000 operations: %v", kind, kinds)
		}
	}
	if replays == 0 {
		t.Error("no replays in 5000 operations")
	}
}

func TestDispatchSchedulesEveryTickBeforeTheEnd(t *testing.T) {
	ticks := make(chan time.Time, 4)
	start := time.Now()
	go dispatch(context.Background(), ticks, start, start.Add(20*time.Millisecond), 1000)
	var got []time.Time
	for at := range ticks {
		got = append(got, at)
	}
	if len(got) != 20 {
		t.Fatalf("%d ticks; want 20 at 1000/s over 20ms", len(got))
	}
	for i, at := range got {
		if want := start.Add(time.Duration(i) * time.Millisecond); !at.Equal(want) {
			t.Fatalf("tick %d at %s; want %s", i, at.Sub(start), want.Sub(start))
		}
	}
}

func TestTally(t *testing.T) {
	samples := []sample{{outcome: "processed"}, {outcome: "processed"}, {outcome: "unavailable"}, {outcome: "conflict:X"}, {outcome: "replayed"}}
	outcomes, failures, conflicts := tally(samples)
	want := []outcome{{"processed", 2}, {"conflict:X", 1}, {"replayed", 1}, {"unavailable", 1}}
	if !reflect.DeepEqual(outcomes, want) || failures != 1 || conflicts != 1 {
		t.Fatalf("tally = %v, %d failures, %d conflicts", outcomes, failures, conflicts)
	}
}
