package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/interruption"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func TestCSV(t *testing.T) {
	rep := Build([]rules.Result{
		{Kind: "Deployment", Namespace: "shop", Name: "web, v2", Replicas: 3, CPUMilli: 1500,
			Score: 85, Verdict: rules.Caveats, Findings: []rules.Finding{{Rule: "SR006"}, {Rule: "SR009"}}},
		{Kind: "StatefulSet", Namespace: "shop", Name: "db", Replicas: 1, CPUMilli: 2000,
			OnSpot: true, Score: 15, Verdict: rules.NotReady},
	}, 0)
	var buf bytes.Buffer
	if err := CSV(&buf, rep); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	if len(rows) != 3 || len(rows[0]) != 11 {
		t.Fatalf("want header plus 2 rows of 11 columns, got %d rows", len(rows))
	}
	got := strings.Join(rows[1], "|")
	want := "ready-with-caveats|85|shop|Deployment|web, v2|3|1500|false|SR006;SR009|0|"
	if got != want {
		t.Fatalf("row = %q, want %q", got, want)
	}
	if rows[2][7] != "true" || rows[2][8] != "" {
		t.Fatalf("second row = %v", rows[2])
	}
}

func TestMarkdown(t *testing.T) {
	results := []rules.Result{
		{Kind: "Deployment", Namespace: "shop", Name: "a|b", Replicas: 1, CPUMilli: 100, Score: 15, Verdict: rules.NotReady,
			Findings: []rules.Finding{{Rule: "SR001"}, {Rule: "SR006"}}},
		{Kind: "Deployment", Namespace: "shop", Name: "web", Replicas: 3, CPUMilli: 900, Score: 100, Verdict: rules.Ready},
	}
	var buf bytes.Buffer
	Markdown(&buf, Build(results, 2))
	out := buf.String()
	for _, want := range []string{
		MarkdownMarker, "## Spot readiness", "**2 workloads:** 1 ready, 0 with caveats, 1 not ready (2 skipped)",
		"**90%**", "https://www.robertasoliman.com/en/tools/kubernetes-savings-calculator?tolerant=90",
		"| not-ready | 15 | `shop/deployment/a\\|b` | 1 | SR001, SR006 |", "<summary>Rules</summary>", "**SR009**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown is missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "shop/deployment/web") {
		t.Error("ready workloads should not be listed in the table")
	}
}

func TestMarkdownCapsRows(t *testing.T) {
	var results []rules.Result
	for i := 0; i < maxMarkdownRows+7; i++ {
		results = append(results, rules.Result{Kind: "Deployment", Namespace: "n", Name: fmt.Sprintf("w%03d", i),
			Replicas: 1, Score: 15, Verdict: rules.NotReady, Findings: []rules.Finding{{Rule: "SR001"}}})
	}
	var buf bytes.Buffer
	Markdown(&buf, Build(results, 0))
	if strings.Count(buf.String(), "| not-ready |") != maxMarkdownRows || !strings.Contains(buf.String(), "7 more workloads") {
		t.Fatalf("expected %d rows and a truncation note:\n%s", maxMarkdownRows, buf.String())
	}
	if buf.Len() > 60000 {
		t.Fatalf("report is %d bytes, too close to the 65,536-character comment limit", buf.Len())
	}
}

func TestMarkdownNothingToFix(t *testing.T) {
	var buf bytes.Buffer
	Markdown(&buf, Build([]rules.Result{{Kind: "Deployment", Namespace: "n", Name: "ok", Replicas: 3, CPUMilli: 10, Score: 100, Verdict: rules.Ready}}, 0))
	if !strings.Contains(buf.String(), "No workload needs attention.") {
		t.Fatal(buf.String())
	}
}

func TestTableRuntimeRisk(t *testing.T) {
	rt := &rules.Runtime{Pods: 4, Zones: 1, SpotPods: 4, BlastRadius: 25, Resilience: "low",
		Nodes:    []rules.RuntimeNode{{Name: "ip-1", Zone: "us-east-1a", CapacityType: "spot", InstanceType: "m5.large", Pods: 4}},
		Warnings: []string{"All 4 pods run in the same Availability Zone (us-east-1a)."}}
	rep := Build([]rules.Result{{Kind: "Deployment", Namespace: "shop", Name: "api", Replicas: 4, Score: 85,
		Verdict: rules.Caveats, Runtime: rt}}, 0)
	var buf bytes.Buffer
	Table(&buf, rep, true)
	for _, want := range []string{"RUNTIME RISK", "shop/deployment/api", "ip-1  us-east-1a  spot", "⚠ All 4 pods",
		"Runtime resilience: LOW", "Manifest readiness: 85"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q\n%s", want, buf.String())
		}
	}
}

func TestTableInterruptionHandling(t *testing.T) {
	score := 86
	rep := Build(nil, 0)
	rep.Interruption = &interruption.Assessment{KarpenterDetected: true, InterruptionQueue: interruption.Yes,
		SpotDetected: true, SpotNodes: 3, PDBIssues: 2, SlowTermination: 1, Score: &score, Notes: []string{}}
	var buf bytes.Buffer
	Table(&buf, rep, false)
	for _, want := range []string{"INTERRUPTION HANDLING", "Karpenter detected", "✓", "Spot nodes detected", "✓ (3 nodes)",
		"Node Termination Handler", "PDB issues", "Workloads >120s termination", "Cluster interruption readiness: 86/100"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q\n%s", want, buf.String())
		}
	}
}
