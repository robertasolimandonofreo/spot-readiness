package rules

import (
	"strings"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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

// pdbFor returns a budget that allows one disruption and evicts unhealthy pods, so it triggers
// neither SR014 nor SR015.
func pdbFor(ns string, labels map[string]string) policyv1.PodDisruptionBudget {
	maxUnavailable, always := intstr.FromInt32(1), policyv1.AlwaysAllow
	return policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector:                   &metav1.LabelSelector{MatchLabels: labels},
			MaxUnavailable:             &maxUnavailable,
			UnhealthyPodEvictionPolicy: &always,
		},
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
		{"minAvailable equal to replicas", func(_ *Workload, inv *Inventory) {
			v := intstr.FromInt32(3)
			inv.PDBs[0].Spec.MaxUnavailable, inv.PDBs[0].Spec.MinAvailable = nil, &v
		}, "SR014", Blocker},
		{"minAvailable 100 percent", func(_ *Workload, inv *Inventory) {
			v := intstr.FromString("100%")
			inv.PDBs[0].Spec.MaxUnavailable, inv.PDBs[0].Spec.MinAvailable = nil, &v
		}, "SR014", Blocker},
		{"maxUnavailable zero", func(_ *Workload, inv *Inventory) {
			v := intstr.FromInt32(0)
			inv.PDBs[0].Spec.MaxUnavailable = &v
		}, "SR014", Blocker},
		{"maxUnavailable small percent rounds up to one", func(_ *Workload, inv *Inventory) {
			v := intstr.FromString("10%")
			inv.PDBs[0].Spec.MaxUnavailable = &v
		}, "", ""},
		{"minAvailable below replicas is fine", func(_ *Workload, inv *Inventory) {
			v := intstr.FromInt32(2)
			inv.PDBs[0].Spec.MaxUnavailable, inv.PDBs[0].Spec.MinAvailable = nil, &v
		}, "", ""},
		{"default unhealthy pod eviction policy", func(_ *Workload, inv *Inventory) {
			inv.PDBs[0].Spec.UnhealthyPodEvictionPolicy = nil
		}, "SR015", Caveat},
		{"IfHealthyBudget policy", func(_ *Workload, inv *Inventory) {
			p := policyv1.IfHealthyBudget
			inv.PDBs[0].Spec.UnhealthyPodEvictionPolicy = &p
		}, "SR015", Caveat},
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
	for _, id := range []string{"SR001", "SR004", "SR005", "SR006", "SR009", "SR014", "SR015"} {
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

func selector(w *Workload, kv map[string]string) { w.Template.Spec.NodeSelector = kv }

func requireAffinity(w *Workload, terms ...corev1.NodeSelectorTerm) {
	w.Template.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms},
	}}
}

func in(key string, values ...string) corev1.NodeSelectorTerm {
	return corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
		{Key: key, Operator: corev1.NodeSelectorOpIn, Values: values}}}
}

func TestFlexibilityRules(t *testing.T) {
	zone, itype := "topology.kubernetes.io/zone", "node.kubernetes.io/instance-type"
	cases := []struct {
		name    string
		mutate  func(w *Workload)
		want    map[string]Severity
		score   int
		zones   int
		bucket  string
		famlist string
	}{
		{"unconstrained", func(*Workload) {}, nil, 100, 0, "low", ""},
		{"os and capacity type are neutral", func(w *Workload) {
			selector(w, map[string]string{"kubernetes.io/os": "linux", "karpenter.sh/capacity-type": "spot"})
		}, nil, 100, 0, "low", ""},
		{"single zone", func(w *Workload) { selector(w, map[string]string{zone: "us-east-1a"}) },
			map[string]Severity{"SR016": Caveat}, 65, 1, "medium", ""},
		{"two zones in affinity", func(w *Workload) { requireAffinity(w, in(zone, "us-east-1a", "us-east-1b")) },
			nil, 85, 2, "low", ""},
		{"zones in separate terms are alternatives", func(w *Workload) {
			requireAffinity(w, in(zone, "us-east-1a"), in(zone, "us-east-1b"), in(zone, "us-east-1c"))
		}, nil, 100, 3, "low", ""},
		{"a term without the key lifts the limit", func(w *Workload) {
			requireAffinity(w, in(zone, "us-east-1a"), in("team", "x"))
		}, nil, 100, 0, "low", ""},
		{"specific instance type", func(w *Workload) { selector(w, map[string]string{itype: "m5.large"}) },
			map[string]Severity{"SR017": Caveat}, 55, 0, "medium", "m5"},
		{"a few instance types", func(w *Workload) {
			requireAffinity(w, in(itype, "c5.large", "m5.large", "r5.large"))
		}, map[string]Severity{"SR018": Info}, 70, 0, "medium", "c5,m5,r5"},
		{"many instance types", func(w *Workload) {
			requireAffinity(w, in(itype, "a.x", "b.x", "c.x", "d.x", "e.x", "f.x", "g.x", "h.x", "i.x", "j.x"))
		}, nil, 100, 0, "low", "a,b,c,d,e,f,g,h,i,j"},
		{"one instance family", func(w *Workload) {
			selector(w, map[string]string{"karpenter.k8s.aws/instance-family": "g5"})
		}, map[string]Severity{"SR018": Info}, 70, 0, "medium", "g5"},
		{"gpu", func(w *Workload) {
			w.Template.Spec.Containers[0].Resources.Limits = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}
		}, map[string]Severity{"SR018": Info}, 75, 0, "medium", ""},
		{"hostname", func(w *Workload) { selector(w, map[string]string{"kubernetes.io/hostname": "node-1"}) },
			map[string]Severity{"SR017": Caveat}, 55, 0, "medium", ""},
		{"three pinned aspects", func(w *Workload) {
			selector(w, map[string]string{zone: "us-east-1a", "kubernetes.io/arch": "arm64", "pool": "gpu"})
		}, map[string]Severity{"SR016": Caveat, "SR019": Caveat}, 50, 1, "medium", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := healthy()
			c.mutate(&w)
			r := Evaluate(inventoryWithPDB(), w)
			got := ids(r)
			for id, sev := range c.want {
				if got[id] != sev {
					t.Errorf("want %s as %s, got %v", id, sev, got)
				}
			}
			for _, id := range []string{"SR016", "SR017", "SR018", "SR019"} {
				if _, ok := c.want[id]; !ok {
					if _, found := got[id]; found {
						t.Errorf("unexpected %s", id)
					}
				}
			}
			fl := r.Flexibility
			if fl.Score != c.score || fl.Zones != c.zones || fl.Constraints != c.bucket || strings.Join(fl.Families, ",") != c.famlist {
				t.Errorf("flexibility = %+v, want score %d zones %d %s families %q", fl, c.score, c.zones, c.bucket, c.famlist)
			}
		})
	}
}

func TestRuntimePlacement(t *testing.T) {
	spot := func(name, zone, itype string) Node {
		return Node{Name: name, Zone: zone, CapacityType: "spot", InstanceType: itype}
	}
	cases := []struct {
		name       string
		nodes      []Node
		pods       []string // node of each pod
		resilience string
		blast      int
		zones      int
		warning    string // substring expected in a warning; empty means no warning
	}{
		{"all pods on one node", []Node{spot("n1", "a", "m5.large")}, []string{"n1", "n1", "n1"},
			"low", 100, 1, "All 3 pods run on one node (n1)"},
		{"single pod", []Node{spot("n1", "a", "m5.large")}, []string{"n1"}, "low", 100, 1, "Only 1 pod"},
		{"same zone, several nodes", []Node{spot("n1", "a", "m5.large"), spot("n2", "a", "c5.large"), spot("n3", "a", "r5.large"), spot("n4", "a", "m6.large")},
			[]string{"n1", "n2", "n3", "n4"}, "low", 25, 1, "same Availability Zone (a)"},
		{"half on one node", []Node{spot("n1", "a", "m5.large"), spot("n2", "b", "c5.large")},
			[]string{"n1", "n1", "n2", "n2"}, "medium", 50, 2, "(50%)"},
		{"spread", []Node{spot("n1", "a", "m5.large"), spot("n2", "b", "c5.large"), {Name: "n3", Zone: "c", CapacityType: "on-demand"}},
			[]string{"n1", "n2", "n3"}, "high", 33, 3, ""},
		{"spot pods in one instance type pool", []Node{spot("n1", "a", "m5.large"), spot("n2", "b", "m5.large"), spot("n3", "c", "m5.large")},
			[]string{"n1", "n2", "n3"}, "high", 33, 3, "all m5.large"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv := inventoryWithPDB()
			inv.SetNodes(c.nodes)
			for _, n := range c.pods {
				inv.AddPod("shop", "Deployment", "web", n)
			}
			rt := Evaluate(inv, healthy()).Runtime
			if rt == nil {
				t.Fatal("no runtime analysis")
			}
			if rt.Resilience != c.resilience || rt.BlastRadius != c.blast || rt.Zones != c.zones {
				t.Fatalf("got resilience %s blast %d zones %d, want %s %d %d (%v)", rt.Resilience, rt.BlastRadius, rt.Zones, c.resilience, c.blast, c.zones, rt.Warnings)
			}
			joined := strings.Join(rt.Warnings, "\n")
			if c.warning == "" && joined != "" || !strings.Contains(joined, c.warning) {
				t.Fatalf("warnings = %q, want one containing %q", joined, c.warning)
			}
		})
	}
	// Without a cluster there is no runtime view, and it never changes the manifest score.
	if Evaluate(inventoryWithPDB(), healthy()).Runtime != nil {
		t.Fatal("file mode must not report runtime placement")
	}
}

func TestNodeFromLabels(t *testing.T) {
	n := NodeFromLabels("ip-1", map[string]string{"topology.kubernetes.io/zone": "us-east-1a",
		"node.kubernetes.io/instance-type": "m5.large", "eks.amazonaws.com/capacityType": "SPOT"})
	if n.Zone != "us-east-1a" || n.InstanceType != "m5.large" || n.CapacityType != "spot" {
		t.Fatalf("%+v", n)
	}
	if NodeFromLabels("x", map[string]string{"karpenter.sh/capacity-type": "on-demand"}).CapacityType != "on-demand" {
		t.Fatal("karpenter on-demand")
	}
}

func TestOptions(t *testing.T) {
	stateful := func() Workload {
		w := healthy()
		w.Name, w.Namespace, w.ClaimTemplates = "postgres", "database", true
		return w
	}
	pdb := pdbFor("database", map[string]string{"app": "web"})
	inv := func(opts Options) *Inventory {
		i := &Inventory{PDBs: []policyv1.PodDisruptionBudget{pdb}, Workloads: []Workload{stateful()}}
		AnalyzeWith(i, opts)
		return i
	}

	// Without options SR007 is a caveat.
	i := inv(Options{})
	if got := ids(Evaluate(i, stateful())); got["SR007"] != Caveat {
		t.Fatalf("default: %v", got)
	}
	// Severity override.
	i = inv(Options{Severity: map[string]Severity{"SR007": Info}})
	r := Evaluate(i, stateful())
	if ids(r)["SR007"] != Info || r.Verdict != Ready || r.Score != 95 {
		t.Fatalf("override: %v %s %d", ids(r), r.Verdict, r.Score)
	}
	// Off drops the rule.
	i = inv(Options{Severity: map[string]Severity{"SR007": SeverityOff}})
	if got := ids(Evaluate(i, stateful())); len(got) != 0 {
		t.Fatalf("off: %v", got)
	}
	// Ignore keeps a record, and matches by pattern and kind.
	i = inv(Options{Ignore: []Ignore{{Workload: "database/*", Kind: "deployment", Rules: []string{"SR007"}, Reason: "kept on on-demand"}}})
	r = Evaluate(i, stateful())
	if len(r.Findings) != 0 || len(r.Ignored) != 1 || r.Ignored[0].Reason != "kept on on-demand" || r.Verdict != Ready {
		t.Fatalf("ignore: %+v", r)
	}
	i = inv(Options{Ignore: []Ignore{{Workload: "database/*", Kind: "StatefulSet", Rules: []string{"SR007"}}}})
	if got := ids(Evaluate(i, stateful())); got["SR007"] != Caveat {
		t.Fatalf("a different kind must not match: %v", got)
	}
	// An empty rule list ignores everything for the workload.
	w := stateful()
	w.Replicas = 1
	i = inv(Options{Ignore: []Ignore{{Workload: "database/postgres"}}})
	if r := Evaluate(i, w); len(r.Findings) != 0 || len(r.Ignored) == 0 {
		t.Fatalf("ignore all: %+v", r)
	}
}

func TestMinimums(t *testing.T) {
	zone := "topology.kubernetes.io/zone"
	w := healthy()
	requireAffinity(&w, in(zone, "a", "b"))
	inv := inventoryWithPDB()
	AnalyzeWith(inv, Options{})
	if _, ok := ids(Evaluate(inv, w))["SR016"]; ok {
		t.Fatal("two zones is fine by default")
	}
	AnalyzeWith(inv, Options{MinZones: 3})
	if ids(Evaluate(inv, w))["SR016"] != Caveat {
		t.Fatal("two zones is too few when minZones is 3")
	}
	AnalyzeWith(inv, Options{MinReplicas: 4})
	if ids(Evaluate(inv, healthy()))["SR001"] != Blocker {
		t.Fatal("3 replicas is too few when minReplicas is 4")
	}
}

func TestExcludeNamespaces(t *testing.T) {
	inv := &Inventory{Workloads: []Workload{healthy(), func() Workload { w := healthy(); w.Namespace = "monitoring"; return w }()}}
	got := AnalyzeWith(inv, Options{ExcludeNamespaces: []string{"monitoring"}})
	if len(got) != 1 || got[0].Namespace != "shop" || inv.Skipped != 1 {
		t.Fatalf("got %d results, %d skipped", len(got), inv.Skipped)
	}
}
