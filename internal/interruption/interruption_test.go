package interruption

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/robertasolimandonofreo/spot-readiness/internal/karpenter"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func controller(queue bool) rules.Workload {
	c := corev1.Container{Name: "controller", Image: "public.ecr.aws/karpenter/controller:1.0.0"}
	if queue {
		c.Env = []corev1.EnvVar{{Name: "INTERRUPTION_QUEUE", Value: "my-cluster"}}
	}
	w := rules.Workload{Kind: "Deployment", Namespace: "karpenter", Name: "karpenter"}
	w.Template.Spec.Containers = []corev1.Container{c}
	return w
}

func nth() rules.Workload {
	w := rules.Workload{Kind: "DaemonSet", Namespace: "kube-system", Name: "aws-node-termination-handler"}
	w.Template.Spec.Containers = []corev1.Container{{Name: "nth", Image: "public.ecr.aws/aws-ec2/aws-node-termination-handler:v1.22.0"}}
	return w
}

func spotNodes(provisioner string, n int) []rules.Node {
	var out []rules.Node
	for i := 0; i < n; i++ {
		out = append(out, rules.Node{Name: provisioner + string(rune('a'+i)), CapacityType: "spot", Provisioner: provisioner})
	}
	return out
}

func results(rule ...string) []rules.Result {
	r := rules.Result{}
	for _, id := range rule {
		r.Findings = append(r.Findings, rules.Finding{Rule: id})
	}
	return []rules.Result{r}
}

func TestAssess(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(inv *rules.Inventory)
		results []rules.Result
		score   int // -1: no score
		queue   string
		note    string
	}{
		{"karpenter with queue", func(inv *rules.Inventory) {
			inv.Workloads = []rules.Workload{controller(true)}
			inv.SetNodes(spotNodes("karpenter", 3))
		}, nil, 100, Yes, ""},
		{"example from the docs: queue, 2 PDB issues, 1 slow workload", func(inv *rules.Inventory) {
			inv.Workloads = []rules.Workload{controller(true)}
			inv.SetNodes(spotNodes("karpenter", 3))
		}, results("SR005", "SR014", "SR003"), 86, Yes, "2 workload(s)"},
		{"karpenter without queue or handler", func(inv *rules.Inventory) {
			inv.Workloads = []rules.Workload{controller(false)}
			inv.SetNodes(spotNodes("karpenter", 3))
		}, nil, 60, No, "no interruption queue"},
		{"karpenter without queue but with handler", func(inv *rules.Inventory) {
			inv.Workloads = []rules.Workload{controller(false)}
			inv.DaemonSets = []rules.Workload{nth()}
			inv.SetNodes(spotNodes("karpenter", 3))
		}, nil, 100, No, ""},
		{"queue and handler together", func(inv *rules.Inventory) {
			inv.Workloads = []rules.Workload{controller(true)}
			inv.DaemonSets = []rules.Workload{nth()}
			inv.SetNodes(spotNodes("karpenter", 1))
		}, nil, 95, Yes, "recommends running only one"},
		{"managed node group drains on its own", func(inv *rules.Inventory) {
			inv.SetNodes(spotNodes("eks-managed", 2))
		}, nil, 100, NA, ""},
		{"self-managed spot nodes need the handler", func(inv *rules.Inventory) {
			inv.SetNodes(spotNodes("", 2))
		}, nil, 60, NA, "not launched by Karpenter"},
		{"self-managed spot nodes with the handler", func(inv *rules.Inventory) {
			inv.SetNodes(spotNodes("", 2))
			inv.DaemonSets = []rules.Workload{nth()}
		}, nil, 100, NA, ""},
		{"nodepools without the controller", func(inv *rules.Inventory) {
			p := karpenter.NodePool{Name: "spot"}
			p.Spec.Template.Spec.Requirements = []karpenter.Requirement{{Key: "karpenter.sh/capacity-type", Operator: "In", Values: []string{"spot"}}}
			inv.Karpenter.NodePools = []karpenter.NodePool{p}
		}, nil, 100, Unknown, "could not be checked"},
		{"no spot capacity", func(inv *rules.Inventory) {
			inv.SetNodes([]rules.Node{{Name: "a", CapacityType: "on-demand", Provisioner: "karpenter"}})
		}, nil, -1, NA, "nothing to assess"},
		{"penalties are capped", func(inv *rules.Inventory) {
			inv.Workloads = []rules.Workload{controller(true)}
			inv.SetNodes(spotNodes("karpenter", 1))
		}, results("SR005", "SR005", "SR005", "SR005", "SR005", "SR005", "SR014", "SR003", "SR003", "SR003", "SR003", "SR003", "SR003"), 59, Yes, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv := &rules.Inventory{}
			c.setup(inv)
			a := Assess(inv, c.results)
			switch {
			case c.score < 0 && a.Score != nil:
				t.Errorf("score = %d, want none", *a.Score)
			case c.score >= 0 && (a.Score == nil || *a.Score != c.score):
				t.Errorf("score = %v, want %d (%v)", a.Score, c.score, a.Notes)
			}
			if a.InterruptionQueue != c.queue {
				t.Errorf("queue = %s, want %s", a.InterruptionQueue, c.queue)
			}
			if !strings.Contains(strings.Join(a.Notes, "\n"), c.note) {
				t.Errorf("notes = %q, want one containing %q", a.Notes, c.note)
			}
		})
	}
}

func TestQueueFromFlag(t *testing.T) {
	inv := &rules.Inventory{}
	w := controller(false)
	w.Template.Spec.Containers[0].Args = []string{"--interruption-queue=my-cluster"}
	inv.Workloads = []rules.Workload{w}
	if a := Assess(inv, nil); a.InterruptionQueue != Yes {
		t.Fatalf("queue = %s, want yes", a.InterruptionQueue)
	}
}
