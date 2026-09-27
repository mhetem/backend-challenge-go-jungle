package deploy_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type panel struct {
	ID         int    `json:"id"`
	Type       string `json:"type"`
	Title      string `json:"title"`
	Datasource *struct {
		UID string `json:"uid"`
	} `json:"datasource"`
	GridPos *struct {
		W int `json:"w"`
		H int `json:"h"`
	} `json:"gridPos"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
}

func declaredMetrics(t *testing.T) map[string]bool {
	t.Helper()
	name := regexp.MustCompile(`Name:\s+"([a-z_]+)"`)
	declared := map[string]bool{}
	err := filepath.WalkDir(filepath.Join("..", "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range name.FindAllStringSubmatch(string(src), -1) {
			declared["wallet_"+m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return declared
}

func TestDashboardQueriesMetricsTheServiceExports(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("grafana", "dashboards", "wallet.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		UID    string  `json:"uid"`
		Panels []panel `json:"panels"`
	}
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatalf("dashboard JSON: %v", err)
	}
	datasources, err := os.ReadFile(filepath.Join("grafana", "provisioning", "datasources", "prometheus.yml"))
	if err != nil {
		t.Fatal(err)
	}
	scrape, err := os.ReadFile(filepath.Join("prometheus", "prometheus.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(datasources), "uid: prometheus") || !strings.Contains(string(scrape), "job_name: wallet") {
		t.Fatal(`the provisioned datasource must have uid "prometheus" and the scrape job must be "wallet", as the dashboard queries assume`)
	}

	declared := declaredMetrics(t)
	metric := regexp.MustCompile(`\bwallet_[a-z_]+`)
	ids := map[int]string{}
	for _, p := range dashboard.Panels {
		if other, dup := ids[p.ID]; dup {
			t.Errorf("panels %q and %q share id %d", other, p.Title, p.ID)
		}
		ids[p.ID] = p.Title
		if p.GridPos == nil || p.GridPos.W == 0 || p.GridPos.H == 0 {
			t.Errorf("panel %q has no size", p.Title)
		}
		if p.Type == "row" {
			continue
		}
		if p.Datasource == nil || p.Datasource.UID != "prometheus" || len(p.Targets) == 0 {
			t.Errorf("panel %q must query the provisioned Prometheus datasource", p.Title)
		}
		for _, target := range p.Targets {
			if strings.Contains(target.Expr, "job=") && !strings.Contains(target.Expr, `job="wallet"`) {
				t.Errorf("panel %q selects another job: %s", p.Title, target.Expr)
			}
			for _, name := range metric.FindAllString(target.Expr, -1) {
				base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "_bucket"), "_count"), "_sum")
				if !declared[base] {
					t.Errorf("panel %q queries %s, which no metric in internal/ declares", p.Title, name)
				}
			}
		}
	}
	if dashboard.UID == "" || len(dashboard.Panels) == 0 {
		t.Fatal("the dashboard needs a uid and panels")
	}
}
