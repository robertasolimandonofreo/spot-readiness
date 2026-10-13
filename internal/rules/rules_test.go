package rules

import (
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func i64(v int64) *int64 { return &v }
func i32(v int32) *int32 { return &v }

// healthy returns a workload that triggers no finding, so each test breaks one thing.
func healthy() Workload {
	return Workload{
		Kind: "Deployment", Namespace: "shop", Name: "web", Replicas: 3,
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
			Spec: corev1.PodSpec{
				TerminationGracePeriodSeconds: i64(30),
				TopologySpreadConstraints:     []corev1.TopologySpreadConstraint{{MaxSkew: 1}},
				Containers: []corev1.Container{{
					Name:           "web",
					ReadinessProbe: &corev1.Probe{},
					Lifecycle:      &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{}},
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					}},
				}},
			},
		},
	}
}

func pdbFor(ns string, labels map[string]string) policyv1.PodDisruptionBudget {
	return policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}},
	}
}

func inventoryWithPDB() *Inventory {
	return &Inventory{PDBs: []policyv1.PodDisruptionBudget{pdbFor("shop", map[string]string{"app": "web"})}}
}

func ids(r Result) map[string]Severity {
	out := map[string]Severity{}
	for _, f := range r.Findings {
		out[f.Rule] = f.Severity
	}
	return out
}

func TestHealthyWorkloadIsReady(t *testing.T) {
	r := Evaluate(inventoryWithPDB(), healthy())
	if r.Verdict != Ready || r.Score != 100 || len(r.Findings) != 0 {
		t.Fatalf("want ready/100/no findings, got %s/%d/%v", r.Verdict, r.Score, r.Findings)
	}
	if r.CPUMilli != 1500 {
		t.Fatalf("cpu = %d, want 1500", r.CPUMilli)
	}
}

func TestRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(w *Workload, inv *Inventory)
		rule   string
		sev    Severity
	}{
		{"single replica", func(w *Workload, _ *Inventory) { w.Replicas = 1 }, "SR001", Blocker},
		{"bare pod", func(w *Workload, _ *Inventory) { w.Kind = "Pod" }, "SR001", Blocker},
		{"hostPath", func(w *Workload, _ *Inventory) {
			w.Template.Spec.Volumes = []corev1.Volume{{Name: "h", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/x"}}}}
		}, "SR002", Blocker},
		{"long grace period", func(w *Workload, _ *Inventory) { w.Template.Spec.TerminationGracePeriodSeconds = i64(300) }, "SR003", Caveat},
		{"default grace period is fine", func(w *Workload, _ *Inventory) { w.Template.Spec.TerminationGracePeriodSeconds = nil }, "", ""},
		{"no spread", func(w *Workload, _ *Inventory) { w.Template.Spec.TopologySpreadConstraints = nil }, "SR004", Caveat},
		{"anti-affinity counts as spread", func(w *Workload, _ *Inventory) {
			w.Template.Spec.TopologySpreadConstraints = nil
			w.Template.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{Weight: 1}},
			}}
		}, "", ""},
		{"no pdb", func(_ *Workload, inv *Inventory) { inv.PDBs = nil }, "SR005", Caveat},
		{"pdb in another namespace does not count", func(_ *Workload, inv *Inventory) {
			inv.PDBs = []policyv1.PodDisruptionBudget{pdbFor("other", map[string]string{"app": "web"})}
		}, "SR005", Caveat},
		{"no readiness probe", func(w *Workload, _ *Inventory) { w.Template.Spec.Containers[0].ReadinessProbe = nil }, "SR006", Caveat},
		{"pvc volume", func(w *Workload, _ *Inventory) {
			w.Template.Spec.Volumes = []corev1.Volume{{Name: "d", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "d"}}}}
		}, "SR007", Caveat},
		{"claim templates", func(w *Workload, _ *Inventory) { w.ClaimTemplates = true }, "SR007", Caveat},
		{"no requests", func(w *Workload, _ *Inventory) {
			w.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
		}, "SR008", Info},
		{"no preStop", func(w *Workload, _ *Inventory) { w.Template.Spec.Containers[0].Lifecycle = nil }, "SR009", Info},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, inv := healthy(), inventoryWithPDB()
			c.mutate(&w, inv)
			got := ids(Evaluate(inv, w))
			if c.rule == "" {
				if len(got) != 0 {
					t.Fatalf("want no findings, got %v", got)
				}
				return
			}
			if got[c.rule] != c.sev {
				t.Fatalf("want %s as %s, got %v", c.rule, c.sev, got)
			}
		})
	}
}

func TestHPAMinimumCountsAsReplicas(t *testing.T) {
	w, inv := healthy(), inventoryWithPDB()
	w.Replicas = 1
	inv.HPAs = []autoscalingv2.HorizontalPodAutoscaler{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
			MinReplicas:    i32(2),
		},
	}}
	if _, blocked := ids(Evaluate(inv, w))["SR001"]; blocked {
		t.Fatal("an HPA with minReplicas 2 should lift the single-replica blocker")
	}
}

func TestVerdictsAndScore(t *testing.T) {
	w, inv := healthy(), inventoryWithPDB()
	w.Template.Spec.Containers[0].ReadinessProbe = nil
	if r := Evaluate(inv, w); r.Verdict != Caveats || r.Score != 85 {
		t.Fatalf("one caveat: got %s/%d", r.Verdict, r.Score)
	}
	w.Replicas = 1
	if r := Evaluate(inv, w); r.Verdict != NotReady {
		t.Fatalf("blocker: got %s", r.Verdict)
	}
	// Four caveats together drop below MinCaveatScore.
	w = healthy()
	w.Template.Spec.TerminationGracePeriodSeconds = i64(300)
	w.Template.Spec.TopologySpreadConstraints = nil
	w.Template.Spec.Containers[0].ReadinessProbe = nil
	w.ClaimTemplates = true
	if r := Evaluate(&Inventory{}, w); r.Verdict != NotReady {
		t.Fatalf("many caveats: got %s/%d", r.Verdict, r.Score)
	}
}

func TestOnSpotAndSystemNamespaces(t *testing.T) {
	w := healthy()
	w.Template.Spec.NodeSelector = map[string]string{"karpenter.sh/capacity-type": "spot"}
	if !Evaluate(inventoryWithPDB(), w).OnSpot {
		t.Fatal("expected alreadyOnSpot")
	}
	sys := healthy()
	sys.Namespace = "kube-system"
	inv := &Inventory{Workloads: []Workload{sys, healthy()}}
	if got := Analyze(inv, false); len(got) != 1 || inv.Skipped != 1 {
		t.Fatalf("system namespace should be skipped: %d results, %d skipped", len(got), inv.Skipped)
	}
	if got := Analyze(&Inventory{Workloads: []Workload{sys}}, true); len(got) != 1 {
		t.Fatal("--include-system should keep it")
	}
}

func TestScaledToZeroIsSkipped(t *testing.T) {
	off := healthy()
	off.Replicas = 0
	inv := &Inventory{Workloads: []Workload{off, healthy()}}
	if got := Analyze(inv, false); len(got) != 1 || inv.Skipped != 1 {
		t.Fatalf("scaled to zero should be skipped: %d results, %d skipped", len(got), inv.Skipped)
	}

	// An HPA with a minimum above zero means the workload does run.
	inv = &Inventory{
		Workloads: []Workload{off},
		HPAs: []autoscalingv2.HorizontalPodAutoscaler{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
				MinReplicas:    i32(2),
			},
		}},
	}
	got := Analyze(inv, false)
	if len(got) != 1 {
		t.Fatalf("workload with an HPA minimum should be analyzed, got %d results", len(got))
	}
	// Replicas and CPU use the HPA minimum, not the 0 in spec.replicas.
	if got[0].Replicas != 2 || got[0].CPUMilli != 1000 {
		t.Fatalf("want 2 replicas and 1000m, got %d and %dm", got[0].Replicas, got[0].CPUMilli)
	}
}

func TestPlaceholdersAreSkipped(t *testing.T) {
	for _, image := range []string{"registry.k8s.io/pause:3.9", "pause", "registry.k8s.io/pause@sha256:abc", "localhost:5000/pause:3.9"} {
		ph := healthy()
		ph.Template.Spec.Containers[0].Image = image
		inv := &Inventory{Workloads: []Workload{ph}}
		if got := Analyze(inv, false); len(got) != 0 || inv.Skipped != 1 {
			t.Fatalf("%q should be skipped as a placeholder: %d results, %d skipped", image, len(got), inv.Skipped)
		}
	}
	real := healthy()
	real.Template.Spec.Containers[0].Image = "registry.k8s.io/pause-tools:1"
	if got := Analyze(&Inventory{Workloads: []Workload{real}}, false); len(got) != 1 {
		t.Fatal("an image that merely starts with pause is a real workload")
	}
}

func healthyJob() Workload {
	w := healthy()
	w.Kind = "Job"
	w.Batch = true
	w.Replicas = 1
	w.IgnoresDisruption = true
	return w
}

func TestBatchRules(t *testing.T) {
	zero, six := int32(0), int32(6)
	deadline := int64(3600)
	cases := []struct {
		name   string
		mutate func(w *Workload)
		rule   string
		sev    Severity
	}{
		{"backoffLimit 0", func(w *Workload) { w.BackoffLimit = &zero }, "SR010", Blocker},
		{"default backoffLimit is fine", func(w *Workload) { w.BackoffLimit = nil }, "", ""},
		{"explicit backoffLimit is fine", func(w *Workload) { w.BackoffLimit = &six }, "", ""},
		{"active deadline", func(w *Workload) { w.ActiveDeadlineSeconds = &deadline }, "SR011", Info},
		{"no pod failure policy", func(w *Workload) { w.IgnoresDisruption = false }, "SR012", Info},
		{"hostPath still blocks", func(w *Workload) {
			w.Template.Spec.Volumes = []corev1.Volume{{Name: "h", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/x"}}}}
		}, "SR002", Blocker},
		{"long grace period still applies", func(w *Workload) { w.Template.Spec.TerminationGracePeriodSeconds = i64(300) }, "SR003", Caveat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := healthyJob()
			c.mutate(&w)
			got := ids(Evaluate(&Inventory{}, w))
			if c.rule != "" && got[c.rule] != c.sev {
				t.Fatalf("want %s as %s, got %v", c.rule, c.sev, got)
			}
			if c.rule == "" {
				for id, sev := range got {
					if sev != Info {
						t.Fatalf("unexpected %s %s", id, sev)
					}
				}
			}
		})
	}
}

func TestJobsSkipServiceRules(t *testing.T) {
	w := healthyJob()
	w.Template.Spec.Containers[0].ReadinessProbe = nil
	w.Template.Spec.Containers[0].Lifecycle = nil
	w.Template.Spec.TopologySpreadConstraints = nil
	got := ids(Evaluate(&Inventory{}, w))
	for _, id := range []string{"SR001", "SR004", "SR005", "SR006", "SR009"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s is a service rule and must not apply to a Job", id)
		}
	}
	// A clean Job is ready: the only finding is the informational checkpoint note.
	r := Evaluate(&Inventory{}, healthyJob())
	if r.Verdict != Ready || r.Score != 95 || len(r.Findings) != 1 || r.Findings[0].Rule != "SR013" {
		t.Fatalf("clean job: %s/%d/%v", r.Verdict, r.Score, r.Findings)
	}
}

func TestServiceRulesSkipBatchOnlyRules(t *testing.T) {
	got := ids(Evaluate(inventoryWithPDB(), healthy()))
	for _, id := range []string{"SR010", "SR011", "SR012", "SR013"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s is a Job rule and must not apply to a Deployment", id)
		}
	}
}

func TestKEDAMinimumCountsAsReplicas(t *testing.T) {
	w := healthy()
	w.Replicas = 1
	inv := inventoryWithPDB()
	inv.Scalers = []Scaler{{Namespace: "shop", Kind: "Deployment", Name: "web", MinReplicas: 3}}
	if _, blocked := ids(Evaluate(inv, w))["SR001"]; blocked {
		t.Fatal("a ScaledObject with minReplicaCount 3 should lift the single-replica blocker")
	}
	inv.Scalers[0].MinReplicas = 0
	if got := ids(Evaluate(inv, w)); got["SR001"] != Blocker {
		t.Fatalf("minReplicaCount 0 should block, got %v", got)
	}
}

func TestScalerDefaults(t *testing.T) {
	var spec ScaledObjectSpec
	spec.ScaleTargetRef.Name = "api"
	sc := ScalerFromScaledObject("shop", spec)
	if sc.Kind != "Deployment" || sc.MinReplicas != 0 || sc.Name != "api" {
		t.Fatalf("defaults: %+v", sc)
	}
	w, ok := FromRollout("shop", "web", RolloutSpec{})
	if !ok || w.Kind != "Rollout" || w.Replicas != 1 {
		t.Fatalf("rollout defaults: %+v ok=%v", w, ok)
	}
	if _, ok := FromRollout("shop", "web", RolloutSpec{WorkloadRef: []byte(`{"name":"x"}`)}); ok {
		t.Fatal("a Rollout with workloadRef must be skipped")
	}
}
