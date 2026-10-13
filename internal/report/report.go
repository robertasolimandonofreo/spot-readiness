// Package report renders analysis results as a table or JSON.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

const calculatorURL = "https://www.robertasoliman.com/en/tools/kubernetes-savings-calculator"

type Summary struct {
	Workloads int `json:"workloads"`
	// Batch counts the Jobs and CronJobs among the workloads.
	Batch    int `json:"batch"`
	Ready    int `json:"ready"`
	Caveats  int `json:"readyWithCaveats"`
	NotReady int `json:"notReady"`
	Skipped  int `json:"skipped"`
	// CPUMilli and PotentialCPU cover long-running workloads only. Jobs and CronJobs request CPU
	// only while they run, so counting them like always-on services would overstate their weight.
	CPUMilli     int64 `json:"cpuRequestMillis"`
	PotentialCPU int64 `json:"spotPotentialCpuMillis"`
	// PotentialPercent is the share of requested CPU in long-running workloads that are ready or
	// ready with caveats. It is an upper bound, not a promise: see the README.
	PotentialPercent int    `json:"spotPotentialPercent"`
	CalculatorURL    string `json:"calculatorUrl,omitempty"`
}

type Report struct {
	Summary Summary        `json:"summary"`
	Results []rules.Result `json:"results"`
}

func Build(results []rules.Result, skipped int) Report {
	s := Summary{Workloads: len(results), Skipped: skipped}
	for _, r := range results {
		if r.Batch {
			s.Batch++
		} else {
			s.CPUMilli += r.CPUMilli
		}
		switch r.Verdict {
		case rules.Ready:
			s.Ready++
			if !r.Batch {
				s.PotentialCPU += r.CPUMilli
			}
		case rules.Caveats:
			s.Caveats++
			if !r.Batch {
				s.PotentialCPU += r.CPUMilli
			}
		case rules.NotReady:
			s.NotReady++
		}
	}
	if s.CPUMilli > 0 {
		s.PotentialPercent = int((s.PotentialCPU*100 + s.CPUMilli/2) / s.CPUMilli)
		s.CalculatorURL = fmt.Sprintf("%s?tolerant=%d", calculatorURL, s.PotentialPercent)
	}
	return Report{Summary: s, Results: results}
}

func JSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// CSV writes one row per workload. The summary is not included, so the output stays a plain table
// that spreadsheets and scripts can read. Findings are rule IDs joined with ";".
func CSV(w io.Writer, r Report) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"verdict", "score", "namespace", "kind", "name", "replicas",
		"cpu_request_millis", "already_on_spot", "findings", "flexibility_score", "runtime_resilience"}); err != nil {
		return err
	}
	for _, res := range r.Results {
		ids := make([]string, 0, len(res.Findings))
		for _, f := range res.Findings {
			ids = append(ids, f.Rule)
		}
		resilience := ""
		if res.Runtime != nil {
			resilience = res.Runtime.Resilience
		}
		if err := cw.Write([]string{string(res.Verdict), strconv.Itoa(res.Score), res.Namespace, res.Kind,
			res.Name, strconv.Itoa(int(res.Replicas)), strconv.FormatInt(res.CPUMilli, 10),
			strconv.FormatBool(res.OnSpot), strings.Join(ids, ";"), strconv.Itoa(res.Flexibility.Score), resilience}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// MarkdownMarker is the first line of the Markdown report. A CI step can search for it to find and
// update its own pull request comment instead of posting a new one on every push.
const MarkdownMarker = "<!-- spot-readiness -->"

// maxMarkdownRows keeps the report well under GitHub's 65,536-character comment limit.
const maxMarkdownRows = 50

func mdCell(v string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(v)
}

// Markdown writes a report meant for a pull request comment: a summary, a table of the workloads
// that need attention (worst first, capped), and the rule reference in a collapsed block.
func Markdown(w io.Writer, r Report) {
	s := r.Summary
	fmt.Fprintln(w, MarkdownMarker)
	fmt.Fprintln(w, "## Spot readiness")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "**%d workloads:** %d ready, %d with caveats, %d not ready", s.Workloads, s.Ready, s.Caveats, s.NotReady)
	if s.Skipped > 0 {
		fmt.Fprintf(w, " (%d skipped)", s.Skipped)
	}
	fmt.Fprintln(w, ".")
	if s.CPUMilli > 0 {
		fmt.Fprintf(w, "\nSpot potential: **%d%%** of requested CPU is in long-running workloads that are ready or ready with caveats (Jobs, CronJobs and ScaledJobs are not counted). [Estimate the savings](%s).\n", s.PotentialPercent, s.CalculatorURL)
	}

	var attention []rules.Result
	for _, res := range r.Results {
		if res.Verdict != rules.Ready {
			attention = append(attention, res)
		}
	}
	if len(attention) == 0 {
		fmt.Fprintln(w, "\nNo workload needs attention.")
	} else {
		fmt.Fprintln(w, "\n| Verdict | Score | Workload | Replicas | Findings |")
		fmt.Fprintln(w, "|---|---|---|---|---|")
		for i, res := range attention {
			if i == maxMarkdownRows {
				break
			}
			ids := make([]string, 0, len(res.Findings))
			for _, f := range res.Findings {
				ids = append(ids, f.Rule)
			}
			fmt.Fprintf(w, "| %s | %d | `%s/%s/%s` | %d | %s |\n", res.Verdict, res.Score,
				mdCell(res.Namespace), strings.ToLower(res.Kind), mdCell(res.Name), res.Replicas, strings.Join(ids, ", "))
		}
		if len(attention) > maxMarkdownRows {
			fmt.Fprintf(w, "\n_%d more workloads not shown. Use `-f csv` or `-f json` for the full list._\n", len(attention)-maxMarkdownRows)
		}
	}

	fmt.Fprint(w, "\n<details><summary>Rules</summary>\n\n")
	for _, d := range rules.Descriptions {
		fmt.Fprintf(w, "- **%s** (%s): %s\n", d.ID, d.Severity, d.Text)
	}
	fmt.Fprintln(w, "\n</details>")
	fmt.Fprintln(w, "\n_Ready does not mean safe: this tool only sees configuration, not how your application behaves._")
}

func Table(w io.Writer, r Report, verbose bool) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "VERDICT\tSCORE\tWORKLOAD\tREPLICAS\tCPU\tSPOT\tFINDINGS")
	for _, res := range r.Results {
		spot := "-"
		if res.OnSpot {
			spot = "yes"
		}
		ids := make([]string, 0, len(res.Findings))
		for _, f := range res.Findings {
			ids = append(ids, f.Rule)
		}
		fmt.Fprintf(tw, "%s\t%d\t%s/%s/%s\t%d\t%dm\t%s\t%s\n", res.Verdict, res.Score,
			res.Namespace, strings.ToLower(res.Kind), res.Name, res.Replicas, res.CPUMilli, spot, strings.Join(ids, ","))
	}
	tw.Flush()

	flexibility(w, r.Results)
	runtimeRisk(w, r.Results, verbose)

	if verbose {
		fmt.Fprintln(w)
		for _, res := range r.Results {
			if len(res.Findings) == 0 {
				continue
			}
			fmt.Fprintf(w, "%s/%s/%s\n", res.Namespace, strings.ToLower(res.Kind), res.Name)
			for _, f := range res.Findings {
				fmt.Fprintf(w, "  %s [%s] %s\n", f.Rule, f.Severity, f.Message)
			}
		}
	}

	s := r.Summary
	fmt.Fprintf(w, "\n%d workloads: %d ready, %d with caveats, %d not ready", s.Workloads, s.Ready, s.Caveats, s.NotReady)
	if s.Skipped > 0 {
		fmt.Fprintf(w, " (%d skipped)", s.Skipped)
	}
	fmt.Fprintln(w)
	if s.CPUMilli > 0 {
		fmt.Fprintf(w, "Spot potential: %d%% of requested CPU in long-running workloads (ready or with caveats; Jobs, CronJobs and ScaledJobs excluded).\n", s.PotentialPercent)
		fmt.Fprintf(w, "Estimate the savings: %s\n", s.CalculatorURL)
	}
	fmt.Fprintln(w, "Ready does not mean safe: this tool only sees configuration, not how your application behaves.")
}

// flexibility prints the workloads whose scheduling constraints narrow their Spot capacity pools,
// least flexible first. Workloads with no such constraint (score 100) are left out.
func flexibility(w io.Writer, results []rules.Result) {
	var rows []rules.Result
	for _, res := range results {
		if res.Flexibility.Score < 100 {
			rows = append(rows, res)
		}
	}
	if len(rows) == 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Flexibility.Score < rows[j].Flexibility.Score })
	fmt.Fprint(w, "\nSPOT FLEXIBILITY (workloads with no placement constraints are not listed)\n\n")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKLOAD\tSCORE\tAZS\tFAMILIES\tCONSTRAINTS")
	for _, res := range rows {
		fl := res.Flexibility
		zones, families := "any", "any"
		if fl.Zones > 0 {
			zones = strconv.Itoa(fl.Zones)
		}
		if len(fl.Families) > 0 {
			families = strings.Join(fl.Families, ",")
		}
		fmt.Fprintf(tw, "%s/%s/%s\t%d\t%s\t%s\t%s\n", res.Namespace, strings.ToLower(res.Kind), res.Name,
			fl.Score, zones, families, fl.Constraints)
	}
	tw.Flush()
}

// runtimeRisk prints, for workloads whose running pods are not well spread, where the pods are and
// what one lost node or zone would cost. Well spread workloads (resilience "high" with no warning)
// are left out. With verbose, every listed workload shows its nodes.
func runtimeRisk(w io.Writer, results []rules.Result, verbose bool) {
	var rows []rules.Result
	for _, res := range results {
		if rt := res.Runtime; rt != nil && (rt.Resilience != "high" || len(rt.Warnings) > 0) {
			rows = append(rows, res)
		}
	}
	if len(rows) == 0 {
		return
	}
	order := map[string]int{"low": 0, "medium": 1, "high": 2}
	sort.SliceStable(rows, func(i, j int) bool { return order[rows[i].Runtime.Resilience] < order[rows[j].Runtime.Resilience] })
	fmt.Fprint(w, "\nRUNTIME RISK (where the pods run now; well spread workloads are not listed)\n")
	for _, res := range rows {
		rt := res.Runtime
		fmt.Fprintf(w, "\n%s/%s/%s\n", res.Namespace, strings.ToLower(res.Kind), res.Name)
		fmt.Fprintf(w, "  Pods: %d on %d nodes, %d zones, %d on Spot. Blast radius of one node: %d%%\n",
			rt.Pods, len(rt.Nodes), rt.Zones, rt.SpotPods, rt.BlastRadius)
		if verbose {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "  NODE\tAZ\tCAPACITY\tINSTANCE\tPODS")
			for _, n := range rt.Nodes {
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%d\n", n.Name, dash(n.Zone), dash(n.CapacityType), dash(n.InstanceType), n.Pods)
			}
			tw.Flush()
		}
		for _, warning := range rt.Warnings {
			fmt.Fprintf(w, "  ⚠ %s\n", warning)
		}
		fmt.Fprintf(w, "  Runtime resilience: %s\n  Manifest readiness: %d\n", strings.ToUpper(rt.Resilience), res.Score)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
