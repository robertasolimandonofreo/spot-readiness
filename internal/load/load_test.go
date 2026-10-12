package load

import (
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/report"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func TestSampleManifests(t *testing.T) {
	inv, err := Paths([]string{"../../testdata"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Workloads) != 4 || len(inv.PDBs) != 1 {
		t.Fatalf("got %d workloads and %d PDBs, want 4 and 1", len(inv.Workloads), len(inv.PDBs))
	}
	rep := report.Build(rules.Analyze(inv, false), inv.Skipped)
	s := rep.Summary
	if s.Workloads != 3 || s.Ready != 1 || s.NotReady != 2 || s.Skipped != 1 {
		t.Fatalf("unexpected summary: %+v", s)
	}
	// web (1500m) is the only ready workload: 1500 of 5500m requested CPU.
	if s.PotentialPercent != 27 {
		t.Fatalf("potential = %d%%, want 27%%", s.PotentialPercent)
	}
}

func TestListAndIgnoredDocuments(t *testing.T) {
	inv := &rules.Inventory{}
	doc := `
replicaCount: 3
---
apiVersion: v1
kind: List
items:
  - apiVersion: apps/v1
    kind: Deployment
    metadata: {name: a}
    spec: {replicas: 2, selector: {matchLabels: {a: a}}, template: {metadata: {labels: {a: a}}, spec: {containers: [{name: a, image: x}]}}}
  - apiVersion: apps/v1
    kind: DaemonSet
    metadata: {name: d}
`
	if err := Add(inv, []byte(doc)); err != nil {
		t.Fatal(err)
	}
	if len(inv.Workloads) != 1 || inv.Skipped != 1 || inv.Workloads[0].Namespace != "default" {
		t.Fatalf("got %d workloads, %d skipped, ns %q", len(inv.Workloads), inv.Skipped, inv.Workloads[0].Namespace)
	}
}

func TestUnnamedObjectsAreSkipped(t *testing.T) {
	inv := &rules.Inventory{}
	doc := `
apiVersion: apps/v1
kind: Deployment
metadata: {}
spec: {replicas: 2, selector: {matchLabels: {a: a}}, template: {metadata: {labels: {a: a}}, spec: {containers: [{name: a, image: x}]}}}
---
apiVersion: v1
kind: Pod
spec: {containers: [{name: a, image: x}]}
`
	if err := Add(inv, []byte(doc)); err != nil {
		t.Fatal(err)
	}
	if len(inv.Workloads) != 0 || inv.Skipped != 1 {
		t.Fatalf("got %d workloads, %d skipped; want 0 and 1", len(inv.Workloads), inv.Skipped)
	}
}
