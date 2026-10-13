package plan

import (
	"bytes"
	"strings"
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func result(kind, name string, cpu int64, score int, findings ...rules.Finding) rules.Result {
	return rules.Result{Kind: kind, Namespace: "shop", Name: name, CPUMilli: cpu, Score: score, Replicas: 3, Findings: findings}
}

func finding(rule string, sev rules.Severity) rules.Finding {
	return rules.Finding{Rule: rule, Severity: sev, Message: rule + " message", Remediation: "Fix " + rule + ".\n\n# spec\nkey: value"}
}

func names(es []Entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	return strings.Join(out, ",")
}

func TestBuild(t *testing.T) {
	results := []rules.Result{
		result("Deployment", "api", 4000, 100),
		result("Deployment", "web", 2000, 95, finding("SR009", rules.Info)), // info only: still ready now
		result("Deployment", "small", 500, 100),
		result("Deployment", "checkout", 8000, 70, finding("SR004", rules.Caveat), finding("SR006", rules.Caveat)),
		result("Deployment", "reports", 1000, 85, finding("SR006", rules.Caveat)),
		result("Deployment", "single", 2000, 50, finding("SR001", rules.Blocker)), // a blocker, but fixable
		result("StatefulSet", "postgres", 6000, 35, finding("SR001", rules.Blocker), finding("SR007", rules.Caveat)),
		result("Deployment", "agent", 1000, 50, finding("SR002", rules.Blocker)),
		// A database run as a Deployment: one replica and a volume, so it cannot just get more replicas.
		result("Deployment", "mongodb", 1600, 15, finding("SR001", rules.Blocker), finding("SR007", rules.Caveat)),
		// Persistent volumes with several replicas are only a caveat.
		result("Deployment", "shared", 500, 85, finding("SR007", rules.Caveat)),
		result("StatefulSet", "cache", 1000, 85, finding("SR006", rules.Caveat)), // no volumes: not structural
		{Kind: "CronJob", Namespace: "shop", Name: "nightly", Batch: true, CPUMilli: 9000, Score: 95},
	}
	p := Build(results, []string{"Configure the queue."})
	s := p.Summary
	if s.Workloads != 12 || s.ReadyNow != 4 || s.ReadyAfterChanges != 5 || s.KeepOnDemand != 3 {
		t.Fatalf("summary: %+v", s)
	}
	// Phase 1: highest score first, then the biggest CPU. The CronJob is listed but not counted.
	if got := names(p.Phase1); got != "api,small,nightly,web" {
		t.Fatalf("phase 1 = %s", got)
	}
	// Phase 2: fewest changes first, then the biggest CPU.
	if got := names(p.Phase2); got != "single,cache,reports,shared,checkout" {
		t.Fatalf("phase 2 = %s", got)
	}
	if got := names(p.KeepOnDemand); got != "postgres,mongodb,agent" {
		t.Fatalf("keep = %s", got)
	}
	// CPU covers long-running workloads only: 4000+2000+500 ready now.
	if s.ImmediateCPUMilli != 6500 || s.PotentialCPUMilli != 6500+8000+1000+2000+1000+500 || s.CPUMilli != 4000+2000+500+8000+1000+2000+6000+1000+1000+1600+500 {
		t.Fatalf("cpu: %+v", s)
	}
	if s.ImmediatePercent != 24 || s.PotentialPercent != 69 {
		t.Fatalf("percent: %+v", s)
	}
	checkout := p.Phase2[4]
	if len(checkout.Changes) != 2 || checkout.Changes[0].Action != "Fix SR004." {
		t.Fatalf("changes: %+v", checkout.Changes)
	}
	if p.KeepOnDemand[0].Reason != "SR007 message" {
		t.Fatalf("reason: %q", p.KeepOnDemand[0].Reason)
	}
	var mongo Entry
	for _, e := range p.KeepOnDemand {
		if e.Name == "mongodb" {
			mongo = e
		}
	}
	if !strings.Contains(mongo.Reason, "single replica") {
		t.Fatalf("mongodb reason: %q", mongo.Reason)
	}
	if len(p.KeepOnDemand[0].Changes) != 0 {
		t.Fatal("a workload that stays on on-demand has no changes to make")
	}
}

func TestOutput(t *testing.T) {
	p := Build([]rules.Result{
		result("Deployment", "api", 4000, 100),
		result("Deployment", "checkout", 8000, 85, finding("SR006", rules.Caveat)),
		result("StatefulSet", "postgres", 6000, 35, finding("SR007", rules.Caveat)),
	}, []string{"Configure the queue."})
	var table, suggest, md bytes.Buffer
	Table(&table, p, Options{})
	Table(&suggest, p, Options{Suggest: true})
	Markdown(&md, p)
	for _, want := range []string{"SPOT MIGRATION PLAN", "Total workloads:             3", "Immediately movable to Spot: 4 vCPU (22%)",
		"Potential after changes:     12 vCPU (67%)", "Phase 0: prepare the platform", "- Configure the queue.", "Phase 1: move now",
		"shop/deployment/api  (4 vCPU)", "Phase 2", "SR006  Fix SR006.", "Keep on on-demand", "shop/statefulset/postgres  (6 vCPU)", "SR007 message"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table is missing %q\n%s", want, table.String())
		}
	}
	if strings.Contains(table.String(), "key: value") || !strings.Contains(suggest.String(), "        key: value") {
		t.Error("the snippet must only show with --suggest")
	}
	for _, want := range []string{"## Spot migration plan", "| Ready now | 1 |", "### Phase 1: move now", "- `shop/deployment/api` (4 vCPU)", "### Keep on on-demand"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown is missing %q\n%s", want, md.String())
		}
	}
}

func TestEmpty(t *testing.T) {
	p := Build(nil, nil)
	var buf bytes.Buffer
	Table(&buf, p, Options{})
	if p.Summary.PotentialPercent != 0 || strings.Contains(buf.String(), "Phase 1") {
		t.Fatalf("empty plan: %s", buf.String())
	}
}
