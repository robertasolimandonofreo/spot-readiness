package report

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func res(name string, score int, findings ...string) rules.Result {
	r := rules.Result{Kind: "Deployment", Namespace: "shop", Name: name, Score: score, Findings: []rules.Finding{}}
	for _, f := range findings {
		sev := rules.Caveat
		if f == "SR009" {
			sev = rules.Info
		}
		if f == "SR001" {
			sev = rules.Blocker
		}
		r.Findings = append(r.Findings, rules.Finding{Rule: f, Severity: sev, Message: f + " message"})
	}
	return r
}

func TestCompare(t *testing.T) {
	baseline := []rules.Result{res("api", 85, "SR005"), res("checkout", 85, "SR005"), res("old", 50, "SR006")}
	current := []rules.Result{res("api", 70, "SR005", "SR004"), res("checkout", 100), res("worker", 80, "SR001", "SR009")}
	c := Compare(baseline, current)
	// Baseline mean (85+85+50)/3 = 73, current mean (70+100+80)/3 = 83.
	if c.BaselineScore != 73 || c.Score != 83 || c.Change != 10 {
		t.Fatalf("scores: %+v", c)
	}
	var added []string
	for _, f := range c.New {
		added = append(added, f.Rule+" "+f.Workload)
	}
	// Blockers first, then caveats, then info.
	if got := strings.Join(added, ","); got != "SR001 shop/deployment/worker,SR004 shop/deployment/api,SR009 shop/deployment/worker" {
		t.Fatalf("new = %s", got)
	}
	var resolved []string
	for _, f := range c.Resolved {
		resolved = append(resolved, f.Rule+" "+f.Workload)
	}
	// The finding of a workload that no longer exists counts as resolved too.
	if got := strings.Join(resolved, ","); got != "SR005 shop/deployment/checkout,SR006 shop/deployment/old" {
		t.Fatalf("resolved = %s", got)
	}
	// SR009 is info and does not count as a regression.
	if c.Regressions() != 2 {
		t.Fatalf("regressions = %d, want 2", c.Regressions())
	}
	if Compare(current, current).Regressions() != 0 {
		t.Fatal("identical scans have no regressions")
	}
}

func TestCompareOutput(t *testing.T) {
	rep := Build([]rules.Result{res("api", 78, "SR004")}, 0)
	rep.Comparison = Compare([]rules.Result{res("api", 87, "SR005")}, rep.Results)
	var table, md bytes.Buffer
	Table(&table, rep, false)
	Markdown(&md, rep)
	for _, want := range []string{"Baseline: 87", "Current:  78", "Change:   -9", "NEW FINDINGS", "+ SR004 [caveat] shop/deployment/api", "RESOLVED", "- SR005 [caveat] shop/deployment/api"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table is missing %q\n%s", want, table.String())
		}
	}
	for _, want := range []string{"| 87 | 78 | **-9** |", "**New findings**", "- `SR004` `shop/deployment/api` (caveat)", "**Resolved**"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown is missing %q\n%s", want, md.String())
		}
	}
}

func TestLoadBaseline(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := JSON(&buf, Build([]rules.Result{res("api", 85, "SR005")}, 0)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "baseline.json")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadBaseline(path)
	if err != nil || len(b.Results) != 1 || b.Results[0].Findings[0].Rule != "SR005" {
		t.Fatalf("baseline: %+v, %v", b, err)
	}
	os.WriteFile(path, []byte("not json"), 0o644)
	if _, err := LoadBaseline(path); err == nil {
		t.Fatal("invalid JSON must be an error")
	}
}
