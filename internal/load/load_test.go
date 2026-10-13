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
	// The tool prints English, so the link goes to the English calculator page.
	if want := "https://www.robertasoliman.com/en/tools/kubernetes-savings-calculator?tolerant=27"; s.CalculatorURL != want {
		t.Fatalf("calculator url = %q, want %q", s.CalculatorURL, want)
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

func TestBatchRolloutAndKEDA(t *testing.T) {
	inv := &rules.Inventory{}
	doc := `
apiVersion: batch/v1
kind: CronJob
metadata: {name: nightly, namespace: jobs}
spec:
  schedule: "0 3 * * *"
  jobTemplate:
    spec:
      backoffLimit: 0
      parallelism: 2
      template:
        spec:
          restartPolicy: Never
          containers: [{name: a, image: x, resources: {requests: {cpu: 500m, memory: 1Gi}}}]
---
apiVersion: batch/v1
kind: CronJob
metadata: {name: paused, namespace: jobs}
spec:
  suspend: true
  schedule: "0 3 * * *"
  jobTemplate: {spec: {template: {spec: {containers: [{name: a, image: x}]}}}}
---
apiVersion: batch/v1
kind: Job
metadata: {name: owned, namespace: jobs, ownerReferences: [{apiVersion: batch/v1, kind: CronJob, name: nightly, uid: u}]}
spec: {template: {spec: {containers: [{name: a, image: x}]}}}
---
apiVersion: batch/v1
kind: Job
metadata: {name: migrate, namespace: jobs}
spec:
  podFailurePolicy:
    rules:
      - action: Ignore
        onPodConditions: [{type: DisruptionTarget}]
  template: {spec: {containers: [{name: a, image: x}]}}
---
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata: {name: web, namespace: shop}
spec:
  replicas: 3
  template: {metadata: {labels: {app: web}}, spec: {containers: [{name: web, image: x, resources: {requests: {cpu: 250m, memory: 64Mi}}}]}}
---
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata: {name: ref, namespace: shop}
spec: {workloadRef: {apiVersion: apps/v1, kind: Deployment, name: web}}
---
apiVersion: example.com/v1
kind: Rollout
metadata: {name: other}
spec: {}
---
apiVersion: keda.sh/v1alpha1
kind: ScaledJob
metadata: {name: consumer, namespace: jobs}
spec:
  jobTargetRef:
    backoffLimit: 0
    parallelism: 3
    template: {spec: {restartPolicy: Never, containers: [{name: c, image: x, resources: {requests: {cpu: 100m, memory: 64Mi}}}]}}
---
apiVersion: example.com/v1
kind: ScaledJob
metadata: {name: not-keda}
spec: {}
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: {name: api, namespace: shop}
spec: {scaleTargetRef: {name: api}, minReplicaCount: 2}
`
	if err := Add(inv, []byte(doc)); err != nil {
		t.Fatal(err)
	}
	names := map[string]rules.Workload{}
	for _, w := range inv.Workloads {
		names[w.Kind+"/"+w.Name] = w
	}
	if len(inv.Workloads) != 4 || names["ScaledJob/consumer"].Name == "" || names["CronJob/nightly"].Name == "" || names["Job/migrate"].Name == "" || names["Rollout/web"].Name == "" {
		t.Fatalf("workloads: %v", names)
	}
	if inv.Skipped != 2 { // the suspended CronJob and the Rollout with workloadRef
		t.Fatalf("skipped = %d, want 2", inv.Skipped)
	}
	cron := names["CronJob/nightly"]
	if !cron.Batch || cron.Replicas != 2 || cron.BackoffLimit == nil || *cron.BackoffLimit != 0 {
		t.Fatalf("cronjob: %+v", cron)
	}
	sj := names["ScaledJob/consumer"]
	if !sj.Batch || sj.Replicas != 3 || sj.BackoffLimit == nil || *sj.BackoffLimit != 0 {
		t.Fatalf("scaledjob: %+v", sj)
	}
	if !names["Job/migrate"].IgnoresDisruption {
		t.Fatal("podFailurePolicy that ignores DisruptionTarget was not detected")
	}
	if len(inv.Scalers) != 1 || inv.Scalers[0].Kind != "Deployment" || inv.Scalers[0].MinReplicas != 2 {
		t.Fatalf("scalers: %+v", inv.Scalers)
	}
}
