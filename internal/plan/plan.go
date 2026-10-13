// Package plan turns the analysis into an order of work: what can move to Spot now, what needs
// changes first, what needs a person to look at it and what should stay on on-demand capacity.
package plan

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/robertasolimandonofreo/spot-readiness/internal/report"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

// Change is something to do to a workload before it can move.
type Change struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	// Action is the advice, the first line of the finding's suggested fix.
	Action string `json:"action"`
	// Remediation is the full suggested fix, with its snippet.
	Remediation string `json:"remediation,omitempty"`
}

// Entry is one workload in the plan.
type Entry struct {
	Namespace  string `json:"namespace"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Batch      bool   `json:"batch"`
	Replicas   int32  `json:"replicas"`
	CPUMilli   int64  `json:"cpuRequestMillis"`
	Score      int    `json:"score"`
	Confidence string `json:"confidence"`
	OnSpot     bool   `json:"alreadyOnSpot"`
	// Priority orders phase 2: the CPU that moves, times the score, times how far the verdict can be
	// trusted, divided by the number of changes. Higher goes first. It is zero for jobs, whose CPU is
	// not counted. Rank is the position in phase 2, starting at 1.
	Priority float64 `json:"priority,omitempty"`
	Rank     int     `json:"rank,omitempty"`
	// Changes are the blockers and caveats to fix first (phase 2).
	Changes []Change `json:"changes,omitempty"`
	// Reason says why the workload needs a review or should stay on on-demand capacity.
	Reason string `json:"reason,omitempty"`
	// Review lists what a person should look at before deciding (manual review).
	Review []string `json:"review,omitempty"`
}

type Summary struct {
	Workloads         int `json:"workloads"`
	ReadyNow          int `json:"readyNow"`
	ReadyAfterChanges int `json:"readyAfterChanges"`
	ManualReview      int `json:"manualReview"`
	KeepOnDemand      int `json:"keepOnDemand"`
	// The CPU figures cover long-running workloads only, like the Spot potential of the scan: Jobs
	// and CronJobs request CPU only while they run. The potential does not include the workloads that
	// need a manual review, whose outcome nobody knows yet.
	CPUMilli          int64 `json:"cpuRequestMillis"`
	ImmediateCPUMilli int64 `json:"immediateCpuMillis"`
	PotentialCPUMilli int64 `json:"potentialCpuMillis"`
	ReviewCPUMilli    int64 `json:"manualReviewCpuMillis"`
	ImmediatePercent  int   `json:"immediatePercent"`
	PotentialPercent  int   `json:"potentialPercent"`
}

// Plan is the migration plan.
type Plan struct {
	// SchemaVersion and ToolVersion identify the layout and the release, like in the scan report.
	SchemaVersion string  `json:"schemaVersion"`
	ToolVersion   string  `json:"toolVersion"`
	Summary       Summary `json:"summary"`
	// Prerequisites are cluster-level things to do before the first move (phase 0).
	Prerequisites []string `json:"prerequisites"`
	Phase1        []Entry  `json:"phase1"`
	Phase2        []Entry  `json:"phase2"`
	ManualReview  []Entry  `json:"manualReview"`
	KeepOnDemand  []Entry  `json:"keepOnDemand"`
}

// reviewChecklist is what to look at before moving a workload that holds state.
var reviewChecklist = []string{
	"storage topology (zonal disks, storage class, volume binding)",
	"replication and quorum: how many copies, and whether the rest keeps serving when one is lost",
	"recovery time: how long a replacement pod takes to be useful again",
	"availability zone dependency: whether the volume pins the pod to one zone",
}

// classify decides where a workload goes:
//   - keep on on-demand: data that lives on the node (SR002, hostPath), which no setting fixes;
//   - manual review: persistent volumes on a StatefulSet, or on a workload with one replica (SR007).
//     Whether such a workload can use Spot depends on the application (Kafka with a replication
//     factor of 3 or a PostgreSQL cluster spread over zones can), which the manifest does not show;
//   - ready after changes: blockers or caveats that configuration fixes;
//   - ready now: nothing but info findings.
func classify(r rules.Result) (class string, reason string, changes []Change) {
	seen := map[string]bool{}
	for _, f := range r.Findings {
		seen[f.Rule] = true
	}
	for _, f := range r.Findings {
		if f.Rule == "SR002" {
			return "keep", f.Message, nil
		}
	}
	for _, f := range r.Findings {
		if f.Rule == "SR007" && (r.Kind == "StatefulSet" || seen["SR001"]) {
			reason = "Persistent state detected: " + f.Message
			if r.Kind != "StatefulSet" {
				reason += "; it runs a single replica, and a volume usually attaches to one pod"
			}
			return "review", reason, nil
		}
	}
	for _, f := range r.Findings {
		if f.Severity != rules.Info {
			changes = append(changes, Change{Rule: f.Rule, Severity: string(f.Severity),
				Action: firstLine(f.Remediation), Remediation: f.Remediation})
		}
	}
	if len(changes) > 0 {
		return "changes", "", changes
	}
	return "now", "", nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func percent(part, total int64) int {
	if total == 0 {
		return 0
	}
	return int((part*100 + total/2) / total)
}

// confidenceFactor lowers the priority of workloads whose verdict is less certain.
func confidenceFactor(c string) float64 {
	switch c {
	case rules.ConfidenceLow:
		return 0.4
	case rules.ConfidenceMedium:
		return 0.7
	}
	return 1
}

// priority is the vCPU that would move, times the score, times the confidence, divided by the
// number of changes. It is a way to order the work, not a forecast of savings.
func priority(r rules.Result, changes int) float64 {
	if r.Batch || changes == 0 {
		changes = max(changes, 1)
	}
	if r.Batch {
		return 0
	}
	p := float64(r.CPUMilli) / 1000 * float64(r.Score) / 100 * confidenceFactor(r.Confidence) / float64(changes)
	return math.Round(p*100) / 100
}

// Build groups the workloads and orders each group; prerequisites are added as phase 0.
func Build(results []rules.Result, prerequisites []string) Plan {
	p := Plan{SchemaVersion: report.SchemaVersion, Prerequisites: append([]string{}, prerequisites...),
		Phase1: []Entry{}, Phase2: []Entry{}, ManualReview: []Entry{}, KeepOnDemand: []Entry{}}
	s := &p.Summary
	s.Workloads = len(results)
	for _, r := range results {
		e := Entry{Namespace: r.Namespace, Kind: r.Kind, Name: r.Name, Batch: r.Batch, Replicas: r.Replicas,
			CPUMilli: r.CPUMilli, Score: r.Score, Confidence: r.Confidence, OnSpot: r.OnSpot}
		class, reason, changes := classify(r)
		e.Reason, e.Changes = reason, changes
		e.Priority = priority(r, len(changes))
		counted := !r.Batch
		switch class {
		case "keep":
			p.KeepOnDemand = append(p.KeepOnDemand, e)
			s.KeepOnDemand++
		case "review":
			e.Review = reviewChecklist
			p.ManualReview = append(p.ManualReview, e)
			s.ManualReview++
			if counted {
				s.ReviewCPUMilli += r.CPUMilli
			}
		case "changes":
			p.Phase2 = append(p.Phase2, e)
			s.ReadyAfterChanges++
			if counted {
				s.PotentialCPUMilli += r.CPUMilli
			}
		default:
			p.Phase1 = append(p.Phase1, e)
			s.ReadyNow++
			if counted {
				s.ImmediateCPUMilli += r.CPUMilli
				s.PotentialCPUMilli += r.CPUMilli
			}
		}
		if counted {
			s.CPUMilli += r.CPUMilli
		}
	}
	s.ImmediatePercent = percent(s.ImmediateCPUMilli, s.CPUMilli)
	s.PotentialPercent = percent(s.PotentialCPUMilli, s.CPUMilli)

	byName := func(a, b Entry) bool { return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name }
	// Phase 1: the safest first (the highest score, then the most CPU), a good order for a pilot.
	sort.SliceStable(p.Phase1, func(i, j int) bool {
		a, b := p.Phase1[i], p.Phase1[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.CPUMilli != b.CPUMilli {
			return a.CPUMilli > b.CPUMilli
		}
		return byName(a, b)
	})
	// Phase 2: the best return for the effort first.
	sort.SliceStable(p.Phase2, func(i, j int) bool {
		a, b := p.Phase2[i], p.Phase2[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if len(a.Changes) != len(b.Changes) {
			return len(a.Changes) < len(b.Changes)
		}
		return byName(a, b)
	})
	for i := range p.Phase2 {
		p.Phase2[i].Rank = i + 1
	}
	byCPU := func(es []Entry) {
		sort.SliceStable(es, func(i, j int) bool {
			a, b := es[i], es[j]
			if a.CPUMilli != b.CPUMilli {
				return a.CPUMilli > b.CPUMilli
			}
			return byName(a, b)
		})
	}
	byCPU(p.ManualReview)
	byCPU(p.KeepOnDemand)
	return p
}

// vcpu formats millicores as vCPU, without a trailing ".0".
func vcpu(milli int64) string {
	return strings.TrimSuffix(strconv.FormatFloat(float64(milli)/1000, 'f', 1, 64), ".0") + " vCPU"
}

func (e Entry) name() string {
	return e.Namespace + "/" + strings.ToLower(e.Kind) + "/" + e.Name
}

func (e Entry) detail() string {
	var parts []string
	if e.Batch {
		parts = append(parts, "job")
	} else {
		parts = append(parts, vcpu(e.CPUMilli))
	}
	if e.OnSpot {
		parts = append(parts, "already on Spot")
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// phase2Detail adds what the priority is made of.
func (e Entry) phase2Detail() string {
	return fmt.Sprintf("%s, %s, score %d, confidence %s", e.detail(), plural(len(e.Changes), "change", "changes"), e.Score, e.Confidence)
}

// Options selects what Table adds to the plan.
type Options struct {
	// Suggest prints the full suggested fix of every change in phase 2.
	Suggest bool
}

// Table prints the plan for the terminal.
func Table(w io.Writer, p Plan, opts Options) {
	s := p.Summary
	fmt.Fprint(w, "SPOT MIGRATION PLAN\n\n")
	fmt.Fprintf(w, "Total workloads:             %d\n", s.Workloads)
	fmt.Fprintf(w, "Ready now:                   %d\n", s.ReadyNow)
	fmt.Fprintf(w, "Ready after changes:         %d\n", s.ReadyAfterChanges)
	fmt.Fprintf(w, "Manual review:               %d\n", s.ManualReview)
	fmt.Fprintf(w, "Keep on on-demand:           %d\n", s.KeepOnDemand)
	fmt.Fprintf(w, "\nRequested CPU:               %s\n", vcpu(s.CPUMilli))
	fmt.Fprintf(w, "Immediately movable to Spot: %s (%d%%)\n", vcpu(s.ImmediateCPUMilli), s.ImmediatePercent)
	fmt.Fprintf(w, "Potential after changes:     %s (%d%%)\n", vcpu(s.PotentialCPUMilli), s.PotentialPercent)
	if s.ManualReview > 0 {
		fmt.Fprintf(w, "Waiting for a review:        %s (not in the potential)\n", vcpu(s.ReviewCPUMilli))
	}
	fmt.Fprintln(w, "(CPU of long-running workloads; Jobs and CronJobs are listed but not counted)")

	if len(p.Prerequisites) > 0 {
		fmt.Fprint(w, "\nPhase 0: prepare the platform\n")
		for _, pre := range p.Prerequisites {
			fmt.Fprintf(w, "  - %s\n", pre)
		}
	}
	if len(p.Phase1) > 0 {
		fmt.Fprint(w, "\nPhase 1: move now (safest first)\n")
		for _, e := range p.Phase1 {
			fmt.Fprintf(w, "  %s  (%s, confidence %s)\n", e.name(), e.detail(), e.Confidence)
		}
	}
	if len(p.Phase2) > 0 {
		fmt.Fprint(w, "\nPhase 2: move after these changes (best return for the effort first)\n")
		for _, e := range p.Phase2 {
			fmt.Fprintf(w, "  %d. %s  (%s)\n", e.Rank, e.name(), e.phase2Detail())
			for _, c := range e.Changes {
				fmt.Fprintf(w, "       %s  %s\n", c.Rule, c.Action)
				if opts.Suggest {
					_, snippet, ok := strings.Cut(c.Remediation, "\n\n")
					if !ok {
						continue
					}
					for _, line := range strings.Split(snippet, "\n") {
						fmt.Fprintf(w, "           %s\n", line)
					}
				}
			}
		}
	}
	if len(p.ManualReview) > 0 {
		fmt.Fprint(w, "\nManual review\n")
		for _, e := range p.ManualReview {
			fmt.Fprintf(w, "  %s  (%s, confidence %s)\n    %s\n", e.name(), e.detail(), e.Confidence, e.Reason)
		}
		fmt.Fprint(w, "\n  Before deciding, look at:\n")
		for _, r := range reviewChecklist {
			fmt.Fprintf(w, "    - %s\n", r)
		}
	}
	if len(p.KeepOnDemand) > 0 {
		fmt.Fprint(w, "\nKeep on on-demand\n")
		for _, e := range p.KeepOnDemand {
			fmt.Fprintf(w, "  %s  (%s)\n    %s\n", e.name(), e.detail(), e.Reason)
		}
	}
	fmt.Fprintln(w, "\nThe plan reads configuration only: test each move, start with a few replicas, and keep on-demand capacity as a fallback.")
}

// Markdown prints the plan for a pull request or a document.
func Markdown(w io.Writer, p Plan) {
	s := p.Summary
	fmt.Fprintf(w, "## Spot migration plan\n\n| | |\n|---|---|\n| Total workloads | %d |\n| Ready now | %d |\n| Ready after changes | %d |\n| Manual review | %d |\n| Keep on on-demand | %d |\n| Requested CPU | %s |\n| Immediately movable to Spot | %s (%d%%) |\n| Potential after changes | %s (%d%%) |\n",
		s.Workloads, s.ReadyNow, s.ReadyAfterChanges, s.ManualReview, s.KeepOnDemand, vcpu(s.CPUMilli), vcpu(s.ImmediateCPUMilli), s.ImmediatePercent, vcpu(s.PotentialCPUMilli), s.PotentialPercent)
	if len(p.Prerequisites) > 0 {
		fmt.Fprint(w, "\n### Phase 0: prepare the platform\n\n")
		for _, pre := range p.Prerequisites {
			fmt.Fprintf(w, "- %s\n", pre)
		}
	}
	if len(p.Phase1) > 0 {
		fmt.Fprint(w, "\n### Phase 1: move now\n\n")
		for _, e := range p.Phase1 {
			fmt.Fprintf(w, "- `%s` (%s, confidence %s)\n", e.name(), e.detail(), e.Confidence)
		}
	}
	if len(p.Phase2) > 0 {
		fmt.Fprint(w, "\n### Phase 2: move after these changes\n\n| # | Workload | CPU | Changes | Score | Confidence |\n|---|---|---|---|---|---|\n")
		for _, e := range p.Phase2 {
			cpu := "job"
			if !e.Batch {
				cpu = vcpu(e.CPUMilli)
			}
			var rulesList []string
			for _, c := range e.Changes {
				rulesList = append(rulesList, c.Rule)
			}
			fmt.Fprintf(w, "| %d | `%s` | %s | %s | %d | %s |\n", e.Rank, e.name(), cpu, strings.Join(rulesList, ", "), e.Score, e.Confidence)
		}
	}
	if len(p.ManualReview) > 0 {
		fmt.Fprint(w, "\n### Manual review\n\n")
		for _, e := range p.ManualReview {
			fmt.Fprintf(w, "- `%s` (%s, confidence %s): %s\n", e.name(), e.detail(), e.Confidence, e.Reason)
		}
		fmt.Fprint(w, "\nBefore deciding, look at:\n\n")
		for _, r := range reviewChecklist {
			fmt.Fprintf(w, "- %s\n", r)
		}
	}
	if len(p.KeepOnDemand) > 0 {
		fmt.Fprint(w, "\n### Keep on on-demand\n\n")
		for _, e := range p.KeepOnDemand {
			fmt.Fprintf(w, "- `%s` (%s): %s\n", e.name(), e.detail(), e.Reason)
		}
	}
	fmt.Fprintln(w, "\n_The plan reads configuration only: test each move and keep on-demand capacity as a fallback._")
}
