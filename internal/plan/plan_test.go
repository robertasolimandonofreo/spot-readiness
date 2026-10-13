package plan

import (
	"bytes"
	"strings"
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/pricing"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func result(kind, name string, cpu int64, score int, confidence string, findings ...rules.Finding) rules.Result {
	return rules.Result{Kind: kind, Namespace: "shop", Name: name, CPUMilli: cpu, Score: score, Confidence: confidence, Replicas: 3, Findings: findings}
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
	const high, medium, low = rules.ConfidenceHigh, rules.ConfidenceMedium, rules.ConfidenceLow
	results := []rules.Result{
		result("Deployment", "api", 4000, 100, high),
		result("Deployment", "web", 2000, 95, high, finding("SR009", rules.Info)), // info only: still ready now
		result("Deployment", "small", 500, 100, high),
		result("Deployment", "checkout", 8000, 70, high, finding("SR004", rules.Caveat), finding("SR006", rules.Caveat)),
		result("Deployment", "reports", 1000, 85, high, finding("SR006", rules.Caveat)),
		result("Deployment", "single", 2000, 50, high, finding("SR001", rules.Blocker)), // a blocker, but fixable
		// Persistent state: a StatefulSet, or a Deployment with one replica and a volume.
		result("StatefulSet", "kafka", 6000, 85, low, finding("SR007", rules.Caveat)),
		result("Deployment", "mongodb", 1600, 15, low, finding("SR001", rules.Blocker), finding("SR007", rules.Caveat)),
		// hostPath is the one thing that stays on on-demand.
		result("Deployment", "agent", 1000, 50, low, finding("SR002", rules.Blocker)),
		result("StatefulSet", "cache", 1000, 85, medium, finding("SR006", rules.Caveat)), // no volumes
		// Persistent volumes with several replicas are only a caveat.
		result("Deployment", "shared", 500, 85, low, finding("SR007", rules.Caveat)),
		{Kind: "CronJob", Namespace: "shop", Name: "nightly", Batch: true, CPUMilli: 9000, Score: 95, Confidence: medium},
	}
	p := Build(results, []string{"Configure the queue."})
	s := p.Summary
	if s.Workloads != 12 || s.ReadyNow != 4 || s.ReadyAfterChanges != 5 || s.ManualReview != 2 || s.KeepOnDemand != 1 {
		t.Fatalf("summary: %+v", s)
	}
	// Phase 1: highest score first, then the biggest CPU. The CronJob is listed but not counted.
	if got := names(p.Phase1); got != "api,small,nightly,web" {
		t.Fatalf("phase 1 = %s", got)
	}
	// Phase 2 by priority: vCPU x score x confidence / changes.
	// reports 1.00x0.85x1/1 = 0.85, single 2.00x0.50x1/1 = 1.00, checkout 8.00x0.70x1/2 = 2.80,
	// cache 1.00x0.85x0.7/1 = 0.60, shared 0.50x0.85x0.4/1 = 0.17.
	if got := names(p.Phase2); got != "checkout,single,reports,cache,shared" {
		t.Fatalf("phase 2 = %s", got)
	}
	for i, e := range p.Phase2 {
		if e.Rank != i+1 {
			t.Fatalf("rank of %s = %d, want %d", e.Name, e.Rank, i+1)
		}
	}
	if got := p.Phase2[0].Priority; got != 2.8 {
		t.Fatalf("checkout priority = %v, want 2.8", got)
	}
	// Manual review and keep on on-demand: the biggest CPU first.
	if got := names(p.ManualReview); got != "kafka,mongodb" {
		t.Fatalf("manual review = %s", got)
	}
	if got := names(p.KeepOnDemand); got != "agent" {
		t.Fatalf("keep = %s", got)
	}
	// CPU covers long-running workloads only. The review CPU is apart: it is not in the potential.
	if s.ImmediateCPUMilli != 6500 || s.PotentialCPUMilli != 6500+8000+1000+2000+1000+500 || s.ReviewCPUMilli != 7600 ||
		s.CPUMilli != 4000+2000+500+8000+1000+2000+6000+1600+1000+1000+500 {
		t.Fatalf("cpu: %+v", s)
	}
	if s.ImmediatePercent != 24 || s.PotentialPercent != 69 {
		t.Fatalf("percent: %+v", s)
	}
	if len(p.Phase2[0].Changes) != 2 || p.Phase2[0].Changes[0].Action != "Fix SR004." {
		t.Fatalf("changes: %+v", p.Phase2[0].Changes)
	}
	mongo := p.ManualReview[1]
	if !strings.Contains(mongo.Reason, "Persistent state detected") || !strings.Contains(mongo.Reason, "single replica") || len(mongo.Review) == 0 || len(mongo.Changes) != 0 {
		t.Fatalf("mongodb: %+v", mongo)
	}
	if strings.Contains(p.ManualReview[0].Reason, "single replica") {
		t.Fatalf("a StatefulSet is not blamed for its replicas: %q", p.ManualReview[0].Reason)
	}
	if p.SchemaVersion != "1" {
		t.Fatalf("schema = %q", p.SchemaVersion)
	}
}

func TestOutput(t *testing.T) {
	p := Build([]rules.Result{
		result("Deployment", "api", 4000, 100, "high"),
		result("Deployment", "checkout", 8000, 85, "high", finding("SR006", rules.Caveat)),
		result("StatefulSet", "postgres", 6000, 85, "low", finding("SR007", rules.Caveat)),
		result("Deployment", "agent", 1000, 50, "low", finding("SR002", rules.Blocker)),
	}, []string{"Configure the queue."})
	var table, suggest, md bytes.Buffer
	Table(&table, p, Options{})
	Table(&suggest, p, Options{Suggest: true})
	Markdown(&md, p)
	for _, want := range []string{"SPOT MIGRATION PLAN", "Total workloads:             4", "Manual review:               1",
		"Immediately movable to Spot: 4 vCPU (21%)", "Potential after changes:     12 vCPU (63%)", "Waiting for a review:        6 vCPU (not in the potential)",
		"Phase 0: prepare the platform", "- Configure the queue.", "Phase 1: move now", "shop/deployment/api  (4 vCPU, confidence high)",
		"Phase 2", "1. shop/deployment/checkout  (8 vCPU, 1 change, score 85, confidence high)", "SR006  Fix SR006.",
		"Manual review\n", "shop/statefulset/postgres  (6 vCPU, confidence low)", "Persistent state detected", "Before deciding, look at:", "- recovery time",
		"Keep on on-demand\n", "shop/deployment/agent"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table is missing %q\n%s", want, table.String())
		}
	}
	if strings.Contains(table.String(), "key: value") || !strings.Contains(suggest.String(), "           key: value") {
		t.Error("the snippet must only show with --suggest")
	}
	for _, want := range []string{"## Spot migration plan", "| Ready now | 1 |", "| Manual review | 1 |", "### Phase 1: move now", "- `shop/deployment/api` (4 vCPU, confidence high)",
		"| 1 | `shop/deployment/checkout` | 8 vCPU | SR006 | 85 | high |", "### Manual review", "- recovery time", "### Keep on on-demand"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown is missing %q\n%s", want, md.String())
		}
	}
}

func TestEmpty(t *testing.T) {
	p := Build(nil, nil)
	var buf bytes.Buffer
	Table(&buf, p, Options{})
	if p.Summary.PotentialPercent != 0 || strings.Contains(buf.String(), "Phase 1") || strings.Contains(buf.String(), "Manual review\n") {
		t.Fatalf("empty plan: %s", buf.String())
	}
}

func TestCostOutput(t *testing.T) {
	p := Build([]rules.Result{
		result("Deployment", "api", 40_000, 100, "high"),
		result("Deployment", "checkout", 60_000, 85, "high", finding("SR006", rules.Caveat)),
	}, nil)
	est := pricing.Compute(pricing.Rates{OnDemandVCPUHour: 0.04, SpotVCPUHour: 0.012, Currency: "USD", Source: "prices given on the command line", Notes: []string{"the rate of one instance type stands for the whole cluster"}},
		p.Summary.CPUMilli, p.Summary.ImmediateCPUMilli, p.Summary.PotentialCPUMilli)
	p.Cost = &est
	var table, md bytes.Buffer
	Table(&table, p, Options{})
	Markdown(&md, p)
	// 100 vCPU: 2,920 now, 40 on Spot after phase 1 and all 100 after phase 2.
	for _, want := range []string{"ESTIMATED MONTHLY COMPUTE", "Current on-demand       $2,920", "After phase 1           $2,102", "After phase 2           $876",
		"Estimated savings       $2,044 / month (70.0%)", "ESTIMATE: an estimate from requested CPU only", "not a billing forecast",
		"$0.0400 per vCPU-hour on-demand, $0.0120 on Spot (prices given on the command line)", "Note: the rate of one instance type"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table is missing %q\n%s", want, table.String())
		}
	}
	for _, want := range []string{"### Estimated monthly compute", "| Current on-demand | $2,920 |", "| Estimated savings | $2,044 / month (70.0%) |", "Estimate, not a billing forecast"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown is missing %q\n%s", want, md.String())
		}
	}
	// Without --pricing there is no estimate at all.
	var plain bytes.Buffer
	Table(&plain, Build([]rules.Result{result("Deployment", "api", 1000, 100, "high")}, nil), Options{})
	if strings.Contains(plain.String(), "ESTIMATE") {
		t.Fatal("the estimate must be opt-in")
	}
}

func TestMoney(t *testing.T) {
	cases := map[float64]string{0: "$0.00", 12.5: "$12.50", 99.99: "$99.99", 100: "$100", 8420.4: "$8,420", 1234567.8: "$1,234,568"}
	for v, want := range cases {
		if got := money(v, "USD"); got != want {
			t.Errorf("money(%v) = %s, want %s", v, got, want)
		}
	}
	if got := money(1500, "EUR"); got != "EUR 1,500" {
		t.Errorf("EUR: %s", got)
	}
}
