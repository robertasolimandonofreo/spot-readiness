package rules

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// minSpotInstanceTypes is the instance type diversity AWS recommends for Spot: the more pools a
// workload can use, the less likely a capacity shortage leaves it without nodes.
const minSpotInstanceTypes = 10

// Flexibility describes how many capacity pools a workload can be placed in. Unlike the readiness
// score, it measures the scheduling constraints, not whether the application survives a restart.
type Flexibility struct {
	// Score is 100 for a workload that can run anywhere and drops as constraints narrow the pools.
	Score int `json:"score"`
	// Zones is the number of Availability Zones the workload is limited to, or 0 for any zone.
	Zones int `json:"zones"`
	// Families are the instance families (or categories) it is limited to; empty means any.
	Families []string `json:"families"`
	// Constraints is "low", "medium" or "high".
	Constraints string `json:"constraints"`
}

// dimOfKey maps the node labels that narrow the capacity pools to the dimension they limit.
var dimOfKey = map[string]string{
	"topology.kubernetes.io/zone":                 "zone",
	"failure-domain.beta.kubernetes.io/zone":      "zone",
	"topology.k8s.aws/zone-id":                    "zone",
	"node.kubernetes.io/instance-type":            "type",
	"beta.kubernetes.io/instance-type":            "type",
	"karpenter.k8s.aws/instance-family":           "family",
	"karpenter.k8s.aws/instance-category":         "category",
	"karpenter.k8s.aws/instance-generation":       "generation",
	"karpenter.k8s.aws/instance-gpu-name":         "gpu",
	"karpenter.k8s.aws/instance-gpu-count":        "gpu",
	"karpenter.k8s.aws/instance-gpu-manufacturer": "gpu",
	"karpenter.k8s.aws/instance-gpu-memory":       "gpu",
	"kubernetes.io/arch":                          "arch",
	"beta.kubernetes.io/arch":                     "arch",
	"kubernetes.io/hostname":                      "hostname",
}

// neutralKey reports labels that do not say anything about the capacity pools: the operating
// system, the capacity type (read separately) and Karpenter labels that only shape the instance
// (size, CPU, memory) or point to a NodePool, whose own flexibility the manifests do not show.
func neutralKey(k string) bool {
	return k == "kubernetes.io/os" || k == "beta.kubernetes.io/os" || k == "topology.kubernetes.io/region" ||
		k == "eks.amazonaws.com/capacityType" || strings.HasPrefix(k, "karpenter.") ||
		strings.HasPrefix(k, "node.kubernetes.io/")
}

// placement is what the required scheduling constraints leave open. A nil set means any value.
type placement struct {
	zones, types, families, categories, generations, arches map[string]bool
	gpu, hostname                                           bool
	custom                                                  []string // other labels the node must have
}

// requirements returns one key-to-values map per alternative the scheduler may satisfy: the
// nodeSelector joined with each nodeAffinity term (terms are alternatives, expressions are
// joined). A nil value set means the key is required but its values cannot be listed.
func requirements(spec corev1.PodSpec) []map[string]map[string]bool {
	base := map[string]map[string]bool{}
	for k, v := range spec.NodeSelector {
		base[k] = map[string]bool{v: true}
	}
	var terms []corev1.NodeSelectorTerm
	if a := spec.Affinity; a != nil && a.NodeAffinity != nil && a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		terms = a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	}
	if len(terms) == 0 {
		return []map[string]map[string]bool{base}
	}
	var out []map[string]map[string]bool
	for _, t := range terms {
		req := map[string]map[string]bool{}
		for k, v := range base {
			req[k] = v
		}
		for _, e := range t.MatchExpressions {
			_, known := dimOfKey[e.Key]
			switch {
			case e.Operator == corev1.NodeSelectorOpIn && len(e.Values) > 0:
				set := map[string]bool{}
				for _, v := range e.Values {
					set[v] = true
				}
				if old := req[e.Key]; old != nil {
					for v := range set {
						if !old[v] {
							delete(set, v)
						}
					}
				}
				req[e.Key] = set
			case !known && !neutralKey(e.Key):
				// Exists, NotIn, DoesNotExist, Gt and Lt on a custom label still narrow the nodes.
				if _, ok := req[e.Key]; !ok {
					req[e.Key] = nil
				}
			}
		}
		out = append(out, req)
	}
	return out
}

func smaller(cur, set map[string]bool) map[string]bool {
	if len(set) == 0 || (cur != nil && len(cur) <= len(set)) {
		return cur
	}
	return set
}

func placementOf(w Workload) placement {
	reqs := requirements(w.Template.Spec)
	// A key limits the workload only when every alternative requires it; its allowed values are
	// then the union across alternatives.
	merged := map[string]map[string]bool{}
	for k := range reqs[0] {
		set, all := map[string]bool{}, true
		for _, r := range reqs {
			vals, ok := r[k]
			if !ok {
				all = false
				break
			}
			if vals == nil {
				set = nil
			}
			for v := range vals {
				if set != nil {
					set[v] = true
				}
			}
		}
		if all {
			merged[k] = set
		}
	}
	var p placement
	for k, set := range merged {
		switch dim, known := dimOfKey[k]; {
		case known && dim == "zone":
			p.zones = smaller(p.zones, set)
		case known && dim == "type":
			p.types = smaller(p.types, set)
		case known && dim == "family":
			p.families = smaller(p.families, set)
		case known && dim == "category":
			p.categories = smaller(p.categories, set)
		case known && dim == "generation":
			p.generations = smaller(p.generations, set)
		case known && dim == "arch":
			p.arches = smaller(p.arches, set)
		case known && dim == "gpu":
			p.gpu = true
		case known && dim == "hostname":
			p.hostname = true
		case !known && !neutralKey(k):
			p.custom = append(p.custom, k)
		}
	}
	sort.Strings(p.custom)
	for _, c := range append(append([]corev1.Container{}, w.Template.Spec.InitContainers...), w.Template.Spec.Containers...) {
		for _, list := range []corev1.ResourceList{c.Resources.Requests, c.Resources.Limits} {
			for name := range list {
				if strings.HasSuffix(string(name), "/gpu") {
					p.gpu = true
				}
			}
		}
	}
	return p
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// instanceFamilies lists the families the workload is limited to, read from the instance types or
// the family label, or the categories when only those are set. Empty means any.
func (p placement) instanceFamilies() []string {
	fam := map[string]bool{}
	for t := range p.types {
		fam[strings.SplitN(t, ".", 2)[0]] = true
	}
	for f := range p.families {
		fam[f] = true
	}
	if len(fam) == 0 {
		for c := range p.categories {
			fam[c] = true
		}
	}
	return sortedKeys(fam)
}

// dimensions counts how many aspects of the placement the workload pins down.
func (p placement) dimensions() int {
	n := len(p.custom)
	for _, set := range []map[string]bool{p.zones, p.types, p.families, p.categories, p.generations, p.arches} {
		if set != nil {
			n++
		}
	}
	for _, b := range []bool{p.gpu, p.hostname} {
		if b {
			n++
		}
	}
	return n
}

func countPenalty(n int, steps [][2]int) int {
	for _, s := range steps {
		if n <= s[0] {
			return s[1]
		}
	}
	return 0
}

func (p placement) score() int {
	// Zones, then the narrowest of the instance limits (type, family, category), then the rest.
	penalty := 0
	if p.zones != nil {
		penalty += countPenalty(len(p.zones), [][2]int{{1, 35}, {2, 15}})
	}
	inst := 0
	if p.hostname {
		inst = 45
	}
	if p.types != nil {
		inst = max(inst, countPenalty(len(p.types), [][2]int{{1, 45}, {4, 30}, {minSpotInstanceTypes - 1, 15}}))
	}
	if p.families != nil {
		inst = max(inst, countPenalty(len(p.families), [][2]int{{1, 30}, {2, 20}, {4, 10}}))
	}
	if p.categories != nil {
		inst = max(inst, countPenalty(len(p.categories), [][2]int{{1, 10}}))
	}
	penalty += inst
	if p.generations != nil {
		penalty += 5
	}
	if p.arches != nil {
		penalty += 5
	}
	if p.gpu {
		penalty += 25
	}
	penalty += min(10*len(p.custom), 30)
	return max(100-penalty, 0)
}

func flexibilityOf(w Workload) Flexibility {
	p := placementOf(w)
	s := p.score()
	c := "low"
	switch {
	case s < 50:
		c = "high"
	case s < 80:
		c = "medium"
	}
	return Flexibility{Score: s, Zones: len(p.zones), Families: p.instanceFamilies(), Constraints: c}
}
