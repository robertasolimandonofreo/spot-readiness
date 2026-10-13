// Package plan turns the analysis into an order of work: what can move to Spot now, what needs
// changes first and what should stay on on-demand capacity.
package plan

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

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
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Batch     bool   `json:"batch"`
	Replicas  int32  `json:"replicas"`
	CPUMilli  int64  `json:"cpuRequestMillis"`
	Score     int    `json:"score"`
	OnSpot    bool   `json:"alreadyOnSpot"`
	// Changes are the blockers and caveats to fix first (phase 2).
	Changes []Change `json:"changes,omitempty"`
	// Reason says why the workload should stay on on-demand capacity.
	Reason string `json:"reason,omitempty"`
}

type Summary struct {
	Workloads         int `json:"workloads"`
	ReadyNow          int `json:"readyNow"`
	ReadyAfterChanges int `json:"readyAfterChanges"`
	KeepOnDemand      int `json:"keepOnDemand"`
	// The CPU figures cover long-running workloads only, like the Spot potential of the scan: Jobs
	// and CronJobs request CPU only while they run.
	CPUMilli          int64 `json:"cpuRequestMillis"`
	ImmediateCPUMilli int64 `json:"immediateCpuMillis"`
	PotentialCPUMilli int64 `json:"potentialCpuMillis"`
	ImmediatePercent  int   `json:"immediatePercent"`
	PotentialPercent  int   `json:"potentialPercent"`
}

// Plan is the migration plan.
type Plan struct {
	Summary Summary `json:"summary"`
	// Prerequisites are cluster-level things to do before the first move (phase 0).
	Prerequisites []string `json:"prerequisites"`
	Phase1        []Entry  `json:"phase1"`
	Phase2        []Entry  `json:"phase2"`
	KeepOnDemand  []Entry  `json:"keepOnDemand"`
}

// structural reports the findings that configuration cannot fix: data that lives on the node
// (hostPath), stateful workloads with persistent volumes, and a workload that has persistent
// volumes and runs a single replica (a volume usually attaches to one pod, so adding replicas is not
// a configuration change). Those stay on on-demand capacity.
func structural(kind string, rulesSeen map[string]bool, f rules.Finding) bool {
	return f.Rule == "SR002" || (f.Rule == "SR007" && (kind == "StatefulSet" || rulesSeen["SR001"]))
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

// Build classifies every workload:
//   - ready now: no blocker and no caveat (info findings are advice);
//   - ready after changes: blockers or caveats, none of them structural;
//   - keep on on-demand: a structural finding, see structural.
//
// prerequisites are added as phase 0.
func Build(results []rules.Result, prerequisites []string) Plan {
	p := Plan{Prerequisites: append([]string{}, prerequisites...), Phase1: []Entry{}, Phase2: []Entry{}, KeepOnDemand: []Entry{}}
	s := &p.Summary
	s.Workloads = len(results)
	for _, r := range results {
		e := Entry{Namespace: r.Namespace, Kind: r.Kind, Name: r.Name, Batch: r.Batch, Replicas: r.Replicas,
			CPUMilli: r.CPUMilli, Score: r.Score, OnSpot: r.OnSpot}
		var reasons []string
		seen := map[string]bool{}
		for _, f := range r.Findings {
			seen[f.Rule] = true
		}
		for _, f := range r.Findings {
			switch {
			case structural(r.Kind, seen, f):
				msg := f.Message
				if f.Rule == "SR007" && r.Kind != "StatefulSet" && seen["SR001"] {
					msg += "; it runs a single replica, and a volume usually attaches to one pod"
				}
				reasons = append(reasons, msg)
			case f.Severity != rules.Info:
				e.Changes = append(e.Changes, Change{Rule: f.Rule, Severity: string(f.Severity),
					Action: firstLine(f.Remediation), Remediation: f.Remediation})
			}
		}
		counted := !r.Batch
		switch {
		case len(reasons) > 0:
			e.Changes = nil
			e.Reason = strings.Join(reasons, "; ")
			p.KeepOnDemand = append(p.KeepOnDemand, e)
			s.KeepOnDemand++
		case len(e.Changes) > 0:
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
	// Phase 1: the safest first. Phase 2: the fewest changes first. Biggest CPU breaks ties.
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
	sort.SliceStable(p.Phase2, func(i, j int) bool {
		a, b := p.Phase2[i], p.Phase2[j]
		if len(a.Changes) != len(b.Changes) {
			return len(a.Changes) < len(b.Changes)
		}
		if a.CPUMilli != b.CPUMilli {
			return a.CPUMilli > b.CPUMilli
		}
		return byName(a, b)
	})
	sort.SliceStable(p.KeepOnDemand, func(i, j int) bool {
		a, b := p.KeepOnDemand[i], p.KeepOnDemand[j]
		if a.CPUMilli != b.CPUMilli {
			return a.CPUMilli > b.CPUMilli
		}
		return byName(a, b)
	})
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
	fmt.Fprintf(w, "Keep on on-demand:           %d\n", s.KeepOnDemand)
	fmt.Fprintf(w, "\nRequested CPU:               %s\n", vcpu(s.CPUMilli))
	fmt.Fprintf(w, "Immediately movable to Spot: %s (%d%%)\n", vcpu(s.ImmediateCPUMilli), s.ImmediatePercent)
	fmt.Fprintf(w, "Potential after changes:     %s (%d%%)\n", vcpu(s.PotentialCPUMilli), s.PotentialPercent)
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
			fmt.Fprintf(w, "  %s  (%s)\n", e.name(), e.detail())
		}
	}
	if len(p.Phase2) > 0 {
		fmt.Fprint(w, "\nPhase 2: move after these changes (fewest changes first)\n")
		for _, e := range p.Phase2 {
			fmt.Fprintf(w, "  %s  (%s)\n", e.name(), e.detail())
			for _, c := range e.Changes {
				fmt.Fprintf(w, "    %s  %s\n", c.Rule, c.Action)
				if opts.Suggest {
					_, snippet, _ := strings.Cut(c.Remediation, "\n\n")
					for _, line := range strings.Split(snippet, "\n") {
						if snippet != "" {
							fmt.Fprintf(w, "        %s\n", line)
						}
					}
				}
			}
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
	fmt.Fprintf(w, "## Spot migration plan\n\n| | |\n|---|---|\n| Total workloads | %d |\n| Ready now | %d |\n| Ready after changes | %d |\n| Keep on on-demand | %d |\n| Requested CPU | %s |\n| Immediately movable to Spot | %s (%d%%) |\n| Potential after changes | %s (%d%%) |\n",
		s.Workloads, s.ReadyNow, s.ReadyAfterChanges, s.KeepOnDemand, vcpu(s.CPUMilli), vcpu(s.ImmediateCPUMilli), s.ImmediatePercent, vcpu(s.PotentialCPUMilli), s.PotentialPercent)
	if len(p.Prerequisites) > 0 {
		fmt.Fprint(w, "\n### Phase 0: prepare the platform\n\n")
		for _, pre := range p.Prerequisites {
			fmt.Fprintf(w, "- %s\n", pre)
		}
	}
	if len(p.Phase1) > 0 {
		fmt.Fprint(w, "\n### Phase 1: move now\n\n")
		for _, e := range p.Phase1 {
			fmt.Fprintf(w, "- `%s` (%s)\n", e.name(), e.detail())
		}
	}
	if len(p.Phase2) > 0 {
		fmt.Fprint(w, "\n### Phase 2: move after these changes\n\n")
		for _, e := range p.Phase2 {
			fmt.Fprintf(w, "- `%s` (%s)\n", e.name(), e.detail())
			for _, c := range e.Changes {
				fmt.Fprintf(w, "  - `%s` %s\n", c.Rule, strings.ReplaceAll(c.Action, "|", "\\|"))
			}
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
