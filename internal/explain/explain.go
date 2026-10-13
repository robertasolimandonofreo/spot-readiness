package explain

import (
	"fmt"
	"io"
	"strings"

	"github.com/robertasolimandonofreo/spot-readiness/internal/karpenter"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func block(w io.Writer, title, body string) {
	if body == "" {
		return
	}
	fmt.Fprintf(w, "\n%s\n", title)
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			fmt.Fprintln(w)
		} else {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
}

// docsURL is the documentation link of a rule of either family.
func docsURL(id string) string {
	if strings.HasPrefix(id, "KR") {
		return karpenter.DocsURL(id)
	}
	return rules.DocsURL(id)
}

// WriteRule prints the long form of a rule.
func WriteRule(w io.Writer, r Rule) {
	fmt.Fprintf(w, "%s: %s\n", r.ID, r.Title)
	block(w, "Severity", r.Severity)
	block(w, "Why it matters", r.Why)
	block(w, "Detected when", r.Detected)
	block(w, "Example problematic configuration", r.Bad)
	block(w, "Recommended configuration", r.Good)
	block(w, "References", docsURL(r.ID))
}

// verdictReason says in a sentence why the workload got its verdict.
func verdictReason(res rules.Result) string {
	var blockers, caveats []string
	for _, f := range res.Findings {
		switch f.Severity {
		case rules.Blocker:
			blockers = append(blockers, f.Rule)
		case rules.Caveat:
			caveats = append(caveats, f.Rule)
		}
	}
	switch {
	case len(blockers) > 0:
		return fmt.Sprintf("a blocker makes the workload not ready, whatever the score (%s)", strings.Join(blockers, ", "))
	case res.Verdict == rules.NotReady:
		return fmt.Sprintf("no blocker, but the caveats (%s) took the score below %d, and that many caveats together are not \"ready with caveats\" any more", strings.Join(caveats, ", "), rules.MinCaveatScore)
	case len(caveats) > 0:
		return fmt.Sprintf("there are caveats (%s) but no blocker, and the score stays at %d or more", strings.Join(caveats, ", "), rules.MinCaveatScore)
	}
	return "there is no blocker and no caveat (info findings are advice, not a condition)"
}

// WriteWorkload shows how a workload got its score, verdict and confidence.
func WriteWorkload(w io.Writer, res rules.Result) {
	fmt.Fprintf(w, "%s/%s/%s\n", res.Namespace, strings.ToLower(res.Kind), res.Name)
	fmt.Fprintf(w, "\nScore calculation\n\n  %4d  starting score\n", 100)
	total := 100
	for _, f := range res.Findings {
		p := rules.Penalty[f.Severity]
		total -= p
		fmt.Fprintf(w, "  %4d  %s (%s)  %s\n", -p, f.Rule, f.Severity, f.Message)
	}
	fmt.Fprintf(w, "  ------\n  %4d  final score", res.Score)
	if total < 0 {
		fmt.Fprint(w, " (the score does not go below 0)")
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "\n  A blocker costs %d points, a caveat %d and an info %d.\n", rules.Penalty[rules.Blocker], rules.Penalty[rules.Caveat], rules.Penalty[rules.Info])

	fmt.Fprintf(w, "\nVerdict: %s\n  %s\n", res.Verdict, verdictReason(res))

	fmt.Fprintf(w, "\nConfidence: %s\n", res.Confidence)
	if len(res.ConfidenceReasons) == 0 {
		fmt.Fprintln(w, "  nothing the manifest cannot show lowers it")
	}
	for _, r := range res.ConfidenceReasons {
		fmt.Fprintf(w, "  - %s\n", r)
	}

	if len(res.Ignored) > 0 {
		fmt.Fprint(w, "\nIgnored by the configuration file\n")
		for _, ig := range res.Ignored {
			reason := ig.Reason
			if reason == "" {
				reason = "no reason given"
			}
			fmt.Fprintf(w, "  %s: %s\n", ig.Rule, reason)
		}
	}

	fl := res.Flexibility
	zones := "any zone"
	if fl.Zones > 0 {
		zones = fmt.Sprintf("%d zone(s)", fl.Zones)
	}
	families := "any instance family"
	if len(fl.Families) > 0 {
		families = "families " + strings.Join(fl.Families, ", ")
	}
	fmt.Fprintf(w, "\nSpot flexibility: %d (%s constraints): %s, %s\n  It is separate from the score: only SR016 to SR019 affect the score.\n", fl.Score, fl.Constraints, zones, families)

	if rt := res.Runtime; rt != nil {
		fmt.Fprintf(w, "\nRuntime: %d pod(s) on %d node(s), %d zone(s), blast radius of one node %d%%, resilience %s\n", rt.Pods, len(rt.Nodes), rt.Zones, rt.BlastRadius, rt.Resilience)
		for _, warn := range rt.Warnings {
			fmt.Fprintf(w, "  - %s\n", warn)
		}
	}
}
