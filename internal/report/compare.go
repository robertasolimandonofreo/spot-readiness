package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

// Change is a finding that appeared or disappeared between the baseline and the current scan.
type Change struct {
	Rule     string         `json:"rule"`
	Severity rules.Severity `json:"severity"`
	Workload string         `json:"workload"` // namespace/kind/name, kind in lower case
	Message  string         `json:"message"`
}

// Comparison is the current scan measured against a baseline scan, for example the base branch of
// a pull request.
type Comparison struct {
	// BaselineScore and Score are the average workload scores of the two scans.
	BaselineScore int `json:"baselineScore"`
	Score         int `json:"score"`
	Change        int `json:"change"`
	// New are findings the baseline did not have; Resolved are findings it had and the current scan
	// does not. A finding is identified by workload and rule.
	New      []Change `json:"new"`
	Resolved []Change `json:"resolved"`
}

// Regressions counts the new findings that are blockers or caveats. Info findings do not count:
// they are advice, not a reason to fail a build.
func (c *Comparison) Regressions() int {
	n := 0
	for _, f := range c.New {
		if f.Severity != rules.Info {
			n++
		}
	}
	return n
}

// LoadBaseline reads a report written earlier with -f json. It refuses a report with a schema newer
// than this version understands, because comparing against a layout it cannot read would hide
// regressions instead of reporting them.
func LoadBaseline(path string) (Report, error) {
	var r Report
	data, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("%s is not a spot-readiness JSON report: %w", path, err)
	}
	switch {
	case r.SchemaVersion == "":
		r.SchemaVersion = "1" // written before the field existed, same layout as schema 1
	case r.SchemaVersion != SchemaVersion:
		return r, fmt.Errorf("%s has schema version %q (written by spot-readiness %s), but this version reads schema %q: upgrade spot-readiness or create the baseline again",
			path, r.SchemaVersion, r.ToolVersion, SchemaVersion)
	}
	return r, nil
}

// AverageScore is the mean workload score, rounded, or 100 when there are no workloads.
func AverageScore(results []rules.Result) int {
	if len(results) == 0 {
		return 100
	}
	total := 0
	for _, r := range results {
		total += r.Score
	}
	return (total + len(results)/2) / len(results)
}

func workloadKey(r rules.Result) string {
	return r.Namespace + "/" + strings.ToLower(r.Kind) + "/" + r.Name
}

func findingMap(results []rules.Result) map[string]Change {
	out := map[string]Change{}
	for _, r := range results {
		for _, f := range r.Findings {
			out[workloadKey(r)+"|"+f.Rule] = Change{Rule: f.Rule, Severity: f.Severity, Workload: workloadKey(r), Message: f.Message}
		}
	}
	return out
}

func sorted(m map[string]Change, other map[string]Change) []Change {
	out := []Change{}
	for k, c := range m {
		if _, ok := other[k]; !ok {
			out = append(out, c)
		}
	}
	rank := map[rules.Severity]int{rules.Blocker: 0, rules.Caveat: 1, rules.Info: 2}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		if out[i].Workload != out[j].Workload {
			return out[i].Workload < out[j].Workload
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// Compare measures the current results against the baseline.
func Compare(baseline, current []rules.Result) *Comparison {
	was, now := findingMap(baseline), findingMap(current)
	c := &Comparison{BaselineScore: AverageScore(baseline), Score: AverageScore(current),
		New: sorted(now, was), Resolved: sorted(was, now)}
	c.Change = c.Score - c.BaselineScore
	return c
}

func signed(n int) string {
	if n > 0 {
		return fmt.Sprintf("+%d", n)
	}
	return fmt.Sprintf("%d", n)
}

// comparisonTable prints the comparison for the terminal.
func comparisonTable(w io.Writer, c *Comparison) {
	if c == nil {
		return
	}
	fmt.Fprintf(w, "\nCOMPARISON WITH BASELINE\n\nBaseline: %d\nCurrent:  %d\nChange:   %s\n", c.BaselineScore, c.Score, signed(c.Change))
	if len(c.New) > 0 {
		fmt.Fprint(w, "\nNEW FINDINGS\n\n")
		for _, f := range c.New {
			fmt.Fprintf(w, "+ %s [%s] %s\n", f.Rule, f.Severity, f.Workload)
		}
	}
	if len(c.Resolved) > 0 {
		fmt.Fprint(w, "\nRESOLVED\n\n")
		for _, f := range c.Resolved {
			fmt.Fprintf(w, "- %s [%s] %s\n", f.Rule, f.Severity, f.Workload)
		}
	}
	if len(c.New) == 0 && len(c.Resolved) == 0 {
		fmt.Fprintln(w, "\nNo findings were added or resolved.")
	}
}

// comparisonMarkdown prints the comparison for a pull request comment.
func comparisonMarkdown(w io.Writer, c *Comparison) {
	if c == nil {
		return
	}
	fmt.Fprintf(w, "\n### Compared with the base branch\n\n| Baseline | PR | Change |\n|---|---|---|\n| %d | %d | **%s** |\n", c.BaselineScore, c.Score, signed(c.Change))
	list := func(title string, changes []Change) {
		if len(changes) == 0 {
			return
		}
		fmt.Fprintf(w, "\n**%s**\n\n", title)
		for i, f := range changes {
			if i == maxMarkdownRows {
				fmt.Fprintf(w, "\n_%d more not shown._\n", len(changes)-maxMarkdownRows)
				break
			}
			fmt.Fprintf(w, "- `%s` %s (%s)\n", f.Rule, mdCell("`"+f.Workload+"`"), f.Severity)
		}
	}
	list("New findings", c.New)
	list("Resolved", c.Resolved)
	if len(c.New) == 0 && len(c.Resolved) == 0 {
		fmt.Fprintln(w, "\nNo findings were added or resolved.")
	}
}
