// Package karpenter checks Karpenter NodePools for the diversity Spot needs. It is separate from
// the workload rules: those tell whether an application can run on Spot, these whether the
// platform that provides the nodes can keep supplying Spot capacity.
package karpenter

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/intstr"
)

// Requirement is one entry of spec.template.spec.requirements.
type Requirement struct {
	Key       string   `json:"key"`
	Operator  string   `json:"operator"`
	Values    []string `json:"values"`
	MinValues *int     `json:"minValues"`
}

// Budget is one entry of spec.disruption.budgets.
type Budget struct {
	Nodes    *intstr.IntOrString `json:"nodes"`
	Reasons  []string            `json:"reasons"`
	Schedule string              `json:"schedule"`
}

// NodePoolSpec is the part of a NodePool (karpenter.sh/v1 or v1beta1) that matters here.
type NodePoolSpec struct {
	Template struct {
		Spec struct {
			Requirements []Requirement `json:"requirements"`
			NodeClassRef struct {
				Name string `json:"name"`
			} `json:"nodeClassRef"`
		} `json:"spec"`
	} `json:"template"`
	Disruption struct {
		Budgets []Budget `json:"budgets"`
	} `json:"disruption"`
}

type NodePool struct {
	Name string
	Spec NodePoolSpec
}

// NodeClaim is a node Karpenter launched for a NodePool.
type NodeClaim struct{ Name, NodePool string }

// NodeClass is an EC2NodeClass. Zones are the Availability Zones of its resolved subnets (from
// its status), empty when unknown, for example in manifests that were never applied.
type NodeClass struct {
	Name  string
	Zones []string
}

// Inventory is the Karpenter part of what was loaded from manifests or the cluster.
type Inventory struct {
	NodePools   []NodePool
	NodeClaims  []NodeClaim
	NodeClasses []NodeClass
}

type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Result is the analysis of one NodePool. Zero Zones, InstanceFamilies or InstanceTypes mean the
// pool does not limit them (or the limit cannot be known from the objects).
type Result struct {
	NodePool string `json:"nodePool"`
	// Capacity lists the capacity types the pool may launch, for example "on-demand,spot".
	Capacity         string `json:"capacity"`
	Zones            int    `json:"zones"`
	InstanceFamilies int    `json:"instanceFamilies"`
	InstanceTypes    int    `json:"instanceTypes"`
	// NodeClaims is how many nodes Karpenter currently runs for the pool.
	NodeClaims int `json:"nodeClaims"`
	// Score is nil for pools that cannot launch Spot nodes, which these rules do not judge.
	Score    *int      `json:"score"`
	Findings []Finding `json:"findings"`
}

// Rules explain each check in one line, for reports that list them.
var Rules = []struct{ ID, Severity, Text string }{
	{"KR001", "caveat", "Spot NodePool supports only one Availability Zone"},
	{"KR002", "caveat", "Spot NodePool allows fewer than 3 instance families"},
	{"KR003", "caveat", "Spot NodePool allows fewer than 10 instance types"},
	{"KR004", "caveat", "Spot NodePool pins down 4 or more instance attributes"},
	{"KR005", "info", "Spot NodePool has no explicit disruption budget (the default is 10% of nodes), or a budget that blocks all disruption"},
	{"KR006", "caveat", "NodePool permits only Spot and no other NodePool can launch on-demand nodes"},
	{"KR007", "info", "Spot NodePool does not set minValues to enforce instance diversity"},
}

const (
	minFamilies = 3
	// minTypes is the instance type diversity AWS recommends for Spot.
	minTypes = 10
	maxDims  = 4

	keyCapacity = "karpenter.sh/capacity-type"
	keyZone     = "topology.kubernetes.io/zone"
	keyType     = "node.kubernetes.io/instance-type"
	keyFamily   = "karpenter.k8s.aws/instance-family"
	keyCategory = "karpenter.k8s.aws/instance-category"
)

var penalty = map[string]int{"caveat": 15, "info": 5}

// inValues returns the values a key is limited to by its "In" requirements (several requirements
// on one key all apply, so they are intersected), and whether such a requirement exists.
func inValues(reqs []Requirement, key string) (map[string]bool, bool) {
	var out map[string]bool
	for _, r := range reqs {
		if r.Key != key || r.Operator != "In" {
			continue
		}
		set := map[string]bool{}
		for _, v := range r.Values {
			if out == nil || out[v] {
				set[v] = true
			}
		}
		out = set
	}
	return out, out != nil
}

func hasMinValues(reqs []Requirement) bool {
	for _, r := range reqs {
		if r.MinValues != nil && (r.Key == keyType || r.Key == keyFamily || r.Key == keyCategory) {
			return true
		}
	}
	return false
}

func capacityTypes(reqs []Requirement) map[string]bool {
	if set, ok := inValues(reqs, keyCapacity); ok {
		return set
	}
	return map[string]bool{"on-demand": true} // Karpenter launches on-demand nodes unless told otherwise
}

func joinSorted(set map[string]bool) string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// restrictiveDims counts the instance attributes a pool pins down with a short list of values or
// a numeric bound: category, family, generation, size, CPU, memory, architecture, instance type.
func restrictiveDims(reqs []Requirement) int {
	dims := map[string]bool{}
	for _, r := range reqs {
		if r.Key != "kubernetes.io/arch" && r.Key != keyType && !strings.HasPrefix(r.Key, "karpenter.k8s.aws/instance-") {
			continue
		}
		switch r.Operator {
		case "In":
			if len(r.Values) <= 2 {
				dims[r.Key] = true
			}
		case "Gt", "Gte", "Lt", "Lte":
			dims[r.Key] = true
		}
	}
	return len(dims)
}

// blocksAllDisruption reports a budget that is always active, covers every reason and allows no
// node to be disrupted.
func blocksAllDisruption(b Budget) bool {
	if b.Nodes == nil || b.Schedule != "" || len(b.Reasons) > 0 {
		return false
	}
	return (b.Nodes.Type == intstr.Int && b.Nodes.IntVal == 0) || b.Nodes.StrVal == "0" || b.Nodes.StrVal == "0%"
}

// Analyze evaluates every NodePool, in name order.
func Analyze(inv *Inventory) []Result {
	classZones := map[string][]string{}
	for _, c := range inv.NodeClasses {
		classZones[c.Name] = c.Zones
	}
	claims := map[string]int{}
	for _, c := range inv.NodeClaims {
		claims[c.NodePool]++
	}
	pools := append([]NodePool(nil), inv.NodePools...)
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })

	var out []Result
	for _, p := range pools {
		reqs := p.Spec.Template.Spec.Requirements
		caps := capacityTypes(reqs)
		res := Result{NodePool: p.Name, Capacity: joinSorted(caps), NodeClaims: claims[p.Name], Findings: []Finding{}}

		// Zones: the pool's own list, narrowed to the zones its subnets cover when those are known.
		known := classZones[p.Spec.Template.Spec.NodeClassRef.Name]
		zones, limited := inValues(reqs, keyZone)
		if len(known) > 0 {
			inSubnets := map[string]bool{}
			for _, z := range known {
				if !limited || zones[z] {
					inSubnets[z] = true
				}
			}
			zones, limited = inSubnets, true
		}
		if limited {
			res.Zones = len(zones)
		}

		types, hasTypes := inValues(reqs, keyType)
		families, hasFamilies := inValues(reqs, keyFamily)
		if hasTypes {
			res.InstanceTypes = len(types)
			families, hasFamilies = map[string]bool{}, true
			for t := range types {
				families[strings.SplitN(t, ".", 2)[0]] = true
			}
		}
		if hasFamilies {
			res.InstanceFamilies = len(families)
		}

		if !caps["spot"] {
			out = append(out, res)
			continue
		}
		add := func(id, sev, format string, args ...any) {
			res.Findings = append(res.Findings, Finding{Rule: id, Severity: sev, Message: fmt.Sprintf(format, args...)})
		}
		if res.Zones == 1 {
			add("KR001", "caveat", "supports only one Availability Zone: that zone running out of Spot capacity leaves the pool without nodes")
		}
		if hasFamilies && res.InstanceFamilies < minFamilies {
			add("KR002", "caveat", "allows only %d instance families (at least %d recommended)", res.InstanceFamilies, minFamilies)
		}
		if hasTypes && res.InstanceTypes < minTypes {
			add("KR003", "caveat", "allows only %d instance types (AWS recommends at least %d for Spot)", res.InstanceTypes, minTypes)
		}
		if n := restrictiveDims(reqs); n >= maxDims {
			add("KR004", "caveat", "pins down %d instance attributes (category, family, generation, size, CPU, memory, architecture): few instance types can match", n)
		}
		if !hasMinValues(reqs) {
			add("KR007", "info", "no minValues on instance type, family or category: nothing makes Karpenter keep a minimum of instance diversity")
		}
		if len(caps) == 1 {
			alternative := false
			for _, o := range pools {
				if o.Name != p.Name && capacityTypes(o.Spec.Template.Spec.Requirements)["on-demand"] {
					alternative = true
				}
			}
			if !alternative {
				add("KR006", "caveat", "permits only Spot and no other NodePool can launch on-demand nodes: pods stay Pending when Spot capacity runs out")
			}
		}
		if budgets := p.Spec.Disruption.Budgets; len(budgets) == 0 {
			add("KR005", "info", "no explicit disruption budget: Karpenter applies its default of 10%% of nodes at a time")
		} else {
			for _, b := range budgets {
				if blocksAllDisruption(b) {
					add("KR005", "caveat", "a disruption budget of 0 nodes is always active: consolidation and drift can never replace a node")
					break
				}
			}
		}
		sort.SliceStable(res.Findings, func(i, j int) bool { return res.Findings[i].Rule < res.Findings[j].Rule })
		score := 100
		for _, f := range res.Findings {
			score -= penalty[f.Severity]
		}
		score = max(score, 0)
		res.Score = &score
		out = append(out, res)
	}
	return out
}
