package metrics_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
)

func TestReconciliationDivergencesAreCounted(t *testing.T) {
	reg := metrics.NewRegistry()
	m, err := metrics.NewApp(reg)
	if err != nil {
		t.Fatal(err)
	}
	m.ReconciliationDiverged()
	m.ReconciliationDiverged()
	want := `
# HELP wallet_reconciliation_divergences_total Reconciliations that found a stored balance or version chain diverging from the ledger.
# TYPE wallet_reconciliation_divergences_total counter
wallet_reconciliation_divergences_total 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "wallet_reconciliation_divergences_total"); err != nil {
		t.Fatal(err)
	}
	if _, err := metrics.NewApp(reg); err == nil {
		t.Fatal("registering the app metrics twice on one registry succeeded")
	}
}

func TestRegistryExposesRuntimeMetrics(t *testing.T) {
	families, err := metrics.NewRegistry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
	}
	for _, name := range []string{"go_goroutines", "process_cpu_seconds_total"} {
		if !names[name] {
			t.Fatalf("registry does not expose %s", name)
		}
	}
}
