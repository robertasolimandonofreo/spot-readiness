package rules

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

type Severity string

const (
	Blocker Severity = "blocker"
	Caveat  Severity = "caveat"
	Info    Severity = "info"
)

// Spot gives a two-minute interruption notice, so graceful shutdowns longer than that are cut.
const spotNoticeSeconds = 120

// Penalties subtracted from the 100-point score. Blockers also cap the verdict at "not-ready".
// MinCaveatScore is the lowest score a workload can have and still be "ready-with-caveats".
const MinCaveatScore = 50

var Penalty = map[Severity]int{Blocker: 50, Caveat: 15, Info: 5}

// Descriptions explain each rule in one line, for reports that list the rules.
var Descriptions = []struct{ ID, Severity, Text string }{
	{"SR001", "blocker", "Fewer than 2 replicas (an HPA minimum counts), or a bare Pod"},
	{"SR002", "blocker", "hostPath volumes: the data is lost with the node"},
	{"SR003", "caveat", "terminationGracePeriodSeconds above 120s; Spot gives a 2-minute notice"},
	{"SR004", "caveat", "Several replicas with no topology spread or pod anti-affinity"},
	{"SR005", "caveat", "Several replicas with no matching PodDisruptionBudget"},
	{"SR006", "caveat", "A container without a readinessProbe"},
	{"SR007", "caveat", "Persistent volumes: zonal disks reschedule slower and stay in one zone"},
	{"SR008", "info", "A container without CPU or memory requests"},
	{"SR009", "info", "No preStop hook in any container"},
	{"SR010", "blocker", "Job with backoffLimit 0: a single lost pod fails the whole job"},
	{"SR011", "info", "Job with activeDeadlineSeconds: retries after an interruption count against the deadline"},
	{"SR012", "info", "Job without a podFailurePolicy that ignores DisruptionTarget: interruptions can count as failures"},
	{"SR013", "info", "Job progress is lost on interruption unless the job checkpoints (cannot be verified)"},
}

type Finding struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

type Verdict string

const (
	Ready    Verdict = "ready"
	Caveats  Verdict = "ready-with-caveats"
	NotReady Verdict = "not-ready"
)

type Result struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Replicas  int32  `json:"replicas"`
	// Batch is true for Jobs and CronJobs, which are left out of the Spot potential.
	Batch    bool      `json:"batch"`
	CPUMilli int64     `json:"cpuRequestMillis"`
	OnSpot   bool      `json:"alreadyOnSpot"`
	Score    int       `json:"score"`
	Verdict  Verdict   `json:"verdict"`
	Findings []Finding `json:"findings"`
}

type scope int

const (
	scopeAll     scope = iota // every workload
	scopeService              // long-running services: Deployment, StatefulSet, Rollout, Pod
	scopeBatch                // Jobs and CronJobs
)

type rule struct {
	id    string
	scope scope
	check func(inv *Inventory, w Workload) *Finding
}

func f(id string, s Severity, format string, args ...any) *Finding {
	return &Finding{Rule: id, Severity: s, Message: fmt.Sprintf(format, args...)}
}

func containers(w Workload) []corev1.Container { return w.Template.Spec.Containers }

var all = []rule{
	{"SR001", scopeService, func(inv *Inventory, w Workload) *Finding {
		if w.Kind == "Pod" {
			return f("SR001", Blocker, "bare Pod: nothing recreates it after an interruption")
		}
		if n := inv.MinReplicas(w); n <= 1 {
			return f("SR001", Blocker, "runs with %d replica: an interruption means downtime", n)
		}
		return nil
	}},
	{"SR002", scopeAll, func(_ *Inventory, w Workload) *Finding {
		for _, v := range w.Template.Spec.Volumes {
			if v.HostPath != nil {
				return f("SR002", Blocker, "hostPath volume %q: its data is lost with the node", v.Name)
			}
		}
		return nil
	}},
	{"SR003", scopeAll, func(_ *Inventory, w Workload) *Finding {
		grace := int64(30)
		if g := w.Template.Spec.TerminationGracePeriodSeconds; g != nil {
			grace = *g
		}
		if grace > spotNoticeSeconds {
			return f("SR003", Caveat, "terminationGracePeriodSeconds is %ds, but Spot gives only %ds of notice", grace, spotNoticeSeconds)
		}
		return nil
	}},
	{"SR004", scopeService, func(inv *Inventory, w Workload) *Finding {
		if inv.MinReplicas(w) <= 1 && w.Replicas <= 1 {
			return nil
		}
		s := w.Template.Spec
		anti := s.Affinity != nil && s.Affinity.PodAntiAffinity != nil &&
			(len(s.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 ||
				len(s.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution) > 0)
		if len(s.TopologySpreadConstraints) == 0 && !anti {
			return f("SR004", Caveat, "no topology spread or pod anti-affinity: replicas can land on the same node or zone")
		}
		return nil
	}},
	{"SR005", scopeService, func(inv *Inventory, w Workload) *Finding {
		if inv.MinReplicas(w) <= 1 && w.Replicas <= 1 {
			return nil
		}
		if !inv.HasPDB(w) {
			return f("SR005", Caveat, "no PodDisruptionBudget: draining can remove too many replicas at once")
		}
		return nil
	}},
	{"SR006", scopeService, func(_ *Inventory, w Workload) *Finding {
		for _, c := range containers(w) {
			if c.ReadinessProbe == nil {
				return f("SR006", Caveat, "container %q has no readinessProbe: traffic can reach a pod that is not ready", c.Name)
			}
		}
		return nil
	}},
	{"SR007", scopeAll, func(_ *Inventory, w Workload) *Finding {
		claims := w.ClaimTemplates
		for _, v := range w.Template.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				claims = true
			}
		}
		if claims {
			return f("SR007", Caveat, "uses persistent volumes: zonal disks make rescheduling slower and tied to one zone")
		}
		return nil
	}},
	{"SR008", scopeAll, func(_ *Inventory, w Workload) *Finding {
		for _, c := range containers(w) {
			r := c.Resources.Requests
			if r.Cpu().IsZero() || r.Memory().IsZero() {
				return f("SR008", Info, "container %q is missing CPU or memory requests: the autoscaler cannot bin-pack it well", c.Name)
			}
		}
		return nil
	}},
	{"SR009", scopeService, func(_ *Inventory, w Workload) *Finding {
		for _, c := range containers(w) {
			if c.Lifecycle != nil && c.Lifecycle.PreStop != nil {
				return nil
			}
		}
		return f("SR009", Info, "no preStop hook: in-flight requests may be cut when the node is reclaimed")
	}},
	{"SR010", scopeBatch, func(_ *Inventory, w Workload) *Finding {
		limit := int32(6) // the Kubernetes default
		if w.BackoffLimit != nil {
			limit = *w.BackoffLimit
		}
		if limit == 0 {
			return f("SR010", Blocker, "backoffLimit is 0: a single lost pod fails the whole job")
		}
		return nil
	}},
	{"SR011", scopeBatch, func(_ *Inventory, w Workload) *Finding {
		if w.ActiveDeadlineSeconds != nil {
			return f("SR011", Info, "activeDeadlineSeconds is %ds: time spent retrying after an interruption counts against it", *w.ActiveDeadlineSeconds)
		}
		return nil
	}},
	{"SR012", scopeBatch, func(_ *Inventory, w Workload) *Finding {
		if !w.IgnoresDisruption {
			return f("SR012", Info, "no podFailurePolicy that ignores DisruptionTarget: a pod lost to an interruption can count against backoffLimit")
		}
		return nil
	}},
	{"SR013", scopeBatch, func(_ *Inventory, _ Workload) *Finding {
		return f("SR013", Info, "progress is lost on interruption unless the job checkpoints; the tool cannot verify that")
	}},
}

func onSpot(w Workload) bool {
	sel := w.Template.Spec.NodeSelector
	return sel["karpenter.sh/capacity-type"] == "spot" || sel["eks.amazonaws.com/capacityType"] == "SPOT"
}

func cpuMilli(w Workload, replicas int32) int64 {
	var total int64
	for _, c := range containers(w) {
		total += c.Resources.Requests.Cpu().MilliValue()
	}
	return total * int64(replicas)
}

// Evaluate runs every rule against one workload.
func Evaluate(inv *Inventory, w Workload) Result {
	// An HPA can raise the count above spec.replicas (often 0 or unset when an HPA owns it), so
	// report and weigh the workload by its effective minimum.
	replicas := w.Replicas
	if min := inv.MinReplicas(w); min > replicas {
		replicas = min
	}
	res := Result{Kind: w.Kind, Namespace: w.Namespace, Name: w.Name, Replicas: replicas, Batch: w.Batch,
		CPUMilli: cpuMilli(w, replicas), OnSpot: onSpot(w), Score: 100, Verdict: Ready, Findings: []Finding{}}
	for _, r := range all {
		if (r.scope == scopeBatch && !w.Batch) || (r.scope == scopeService && w.Batch) {
			continue
		}
		if finding := r.check(inv, w); finding != nil {
			res.Findings = append(res.Findings, *finding)
			res.Score -= Penalty[finding.Severity]
			switch {
			case finding.Severity == Blocker:
				res.Verdict = NotReady
			case finding.Severity == Caveat && res.Verdict == Ready:
				res.Verdict = Caveats
			}
		}
	}
	if res.Score < 0 {
		res.Score = 0
	}
	// Too many caveats together are not "ready with caveats" any more.
	if res.Verdict == Caveats && res.Score < MinCaveatScore {
		res.Verdict = NotReady
	}
	return res
}

// isPlaceholder reports workloads made only of "pause" containers. They reserve capacity (for
// example to make Karpenter create nodes ahead of a cutover) and run no application.
func isPlaceholder(w Workload) bool {
	cs := containers(w)
	if len(cs) == 0 {
		return false
	}
	for _, c := range cs {
		image := c.Image
		if i := strings.IndexAny(image, "@"); i >= 0 {
			image = image[:i]
		}
		if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
			image = image[:i]
		}
		if image[strings.LastIndex(image, "/")+1:] != "pause" {
			return false
		}
	}
	return true
}

// Analyze evaluates every workload, skipping system namespaces unless includeSystem is set, and
// workloads scaled to zero (nothing runs, so there is nothing to interrupt) and capacity
// placeholders (pause containers only). Results are ordered worst first.
func Analyze(inv *Inventory, includeSystem bool) []Result {
	var out []Result
	for _, w := range inv.Workloads {
		if !includeSystem && (w.Namespace == "kube-system" || w.Namespace == "kube-public" || w.Namespace == "kube-node-lease") {
			inv.Skipped++
			continue
		}
		if isPlaceholder(w) {
			inv.Skipped++
			continue
		}
		if w.Kind != "Pod" && w.Replicas == 0 && inv.MinReplicas(w) == 0 {
			inv.Skipped++
			continue
		}
		out = append(out, Evaluate(inv, w))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name
	})
	return out
}
