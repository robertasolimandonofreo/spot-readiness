package karpenter

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"
)

func req(key, op string, values ...string) Requirement {
	return Requirement{Key: key, Operator: op, Values: values}
}

func pool(name string, reqs ...Requirement) NodePool {
	var p NodePool
	p.Name = name
	p.Spec.Template.Spec.Requirements = reqs
	p.Spec.Template.Spec.NodeClassRef.Name = "default"
	return p
}

func ids(r Result) string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.Rule)
	}
	return strings.Join(out, ",")
}

func find(t *testing.T, rs []Result, name string) Result {
	t.Helper()
	for _, r := range rs {
		if r.NodePool == name {
			return r
		}
	}
	t.Fatalf("no result for %s", name)
	return Result{}
}

func many(prefix string, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, prefix+string(rune('a'+i))+".large")
	}
	return out
}

func TestRules(t *testing.T) {
	spot := req(keyCapacity, "In", "spot")
	zero := intstr.FromInt32(0)
	ten := intstr.FromString("10%")
	withBudget := func(p NodePool, n *intstr.IntOrString) NodePool {
		p.Spec.Disruption.Budgets = []Budget{{Nodes: n}}
		return p
	}
	onDemand := pool("od", req(keyCapacity, "In", "on-demand"))

	cases := []struct {
		name  string
		pools []NodePool
		want  string // findings of the pool "p"
		score int    // -1 for no score
	}{
		{"healthy spot pool", []NodePool{withBudget(pool("p", spot, Requirement{Key: keyCategory, Operator: "In", Values: []string{"c", "m", "r"}, MinValues: new(int)}), &ten), onDemand}, "", 100},
		{"one zone", []NodePool{withBudget(pool("p", spot, req(keyZone, "In", "a"), Requirement{Key: keyFamily, Operator: "NotIn"}, Requirement{Key: keyCategory, Operator: "Exists", MinValues: new(int)}), &ten), onDemand}, "KR001", 85},
		{"few families and types", []NodePool{withBudget(pool("p", spot, req(keyType, "In", "m5.large", "m5.xlarge", "c5.large"), Requirement{Key: keyFamily, Operator: "Exists", MinValues: new(int)}), &ten), onDemand}, "KR002,KR003", 70},
		{"ten instance types is enough", []NodePool{withBudget(pool("p", spot, req(keyType, "In", append(many("a", 5), many("b", 5)...)...), Requirement{Key: keyType, Operator: "Exists", MinValues: new(int)}), &ten), onDemand}, "", 100},
		{"too many instance attributes", []NodePool{withBudget(pool("p", spot, req(keyCategory, "In", "c"), req("karpenter.k8s.aws/instance-generation", "In", "5"),
			req("karpenter.k8s.aws/instance-size", "In", "large"), req("kubernetes.io/arch", "In", "amd64"), Requirement{Key: keyCategory, Operator: "Exists", MinValues: new(int)}), &ten), onDemand}, "KR004", 85},
		{"no minValues", []NodePool{withBudget(pool("p", spot), &ten), onDemand}, "KR007", 95},
		{"spot only without an alternative", []NodePool{withBudget(pool("p", spot, Requirement{Key: keyCategory, Operator: "Exists", MinValues: new(int)}), &ten)}, "KR006", 85},
		{"spot and on-demand in one pool is its own alternative", []NodePool{withBudget(pool("p", req(keyCapacity, "In", "spot", "on-demand"), Requirement{Key: keyCategory, Operator: "Exists", MinValues: new(int)}), &ten)}, "", 100},
		{"no budget", []NodePool{pool("p", req(keyCapacity, "In", "spot", "on-demand"), Requirement{Key: keyCategory, Operator: "Exists", MinValues: new(int)})}, "KR005", 95},
		{"budget blocking all disruption", []NodePool{withBudget(pool("p", req(keyCapacity, "In", "spot", "on-demand"), Requirement{Key: keyCategory, Operator: "Exists", MinValues: new(int)}), &zero)}, "KR005", 85},
		{"on-demand pools are not judged", []NodePool{pool("p", req(keyZone, "In", "a"))}, "", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := find(t, Analyze(&Inventory{NodePools: c.pools}), "p")
			if ids(r) != c.want {
				t.Errorf("findings = %q, want %q", ids(r), c.want)
			}
			switch {
			case c.score < 0 && r.Score != nil:
				t.Errorf("score = %d, want none", *r.Score)
			case c.score >= 0 && (r.Score == nil || *r.Score != c.score):
				t.Errorf("score = %v, want %d", r.Score, c.score)
			}
		})
	}
}

func TestZonesAndClaims(t *testing.T) {
	p := pool("p", req(keyCapacity, "In", "spot", "on-demand"), req(keyZone, "In", "a", "b", "c"))
	inv := &Inventory{
		NodePools:  []NodePool{p, pool("q", req(keyCapacity, "In", "spot", "on-demand"))},
		NodeClaims: []NodeClaim{{Name: "x", NodePool: "p"}, {Name: "y", NodePool: "p"}},
		// The subnets cover only two of the three zones, and the pool without a zone list gets them all.
		NodeClasses: []NodeClass{{Name: "default", Zones: []string{"a", "b"}}},
	}
	rs := Analyze(inv)
	if r := find(t, rs, "p"); r.Zones != 2 || r.NodeClaims != 2 || r.Capacity != "on-demand,spot" {
		t.Errorf("p = %+v", r)
	}
	if r := find(t, rs, "q"); r.Zones != 2 {
		t.Errorf("q zones = %d, want 2", r.Zones)
	}
	one := &Inventory{NodePools: []NodePool{p}, NodeClasses: []NodeClass{{Name: "default", Zones: []string{"a"}}}}
	if r := find(t, Analyze(one), "p"); !strings.Contains(ids(r), "KR001") {
		t.Errorf("a single subnet zone should trigger KR001, got %q", ids(r))
	}
}

func TestOptions(t *testing.T) {
	p := pool("p", req(keyCapacity, "In", "spot", "on-demand"), req(keyZone, "In", "a", "b"), Requirement{Key: keyCategory, Operator: "Exists", MinValues: new(int)})
	p.Spec.Disruption.Budgets = []Budget{{}}
	inv := &Inventory{NodePools: []NodePool{p}}
	if ids(find(t, AnalyzeWith(inv, Options{}), "p")) != "" {
		t.Fatal("two zones is fine by default")
	}
	if ids(find(t, AnalyzeWith(inv, Options{MinZones: 3}), "p")) != "KR001" {
		t.Fatal("two zones is too few when minZones is 3")
	}
	r := find(t, AnalyzeWith(inv, Options{MinZones: 3, Severity: map[string]string{"KR001": "info"}}), "p")
	if r.Findings[0].Severity != "info" || *r.Score != 95 {
		t.Fatalf("override: %+v %d", r.Findings, *r.Score)
	}
	if ids(find(t, AnalyzeWith(inv, Options{MinZones: 3, Severity: map[string]string{"KR001": "off"}}), "p")) != "" {
		t.Fatal("off drops the rule")
	}
}
