package explain

import (
	"bytes"
	"strings"
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/karpenter"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

// Every rule the tool reports can be explained, with the same severity and a link.
func TestEveryRuleIsExplained(t *testing.T) {
	var all []struct{ ID, Severity string }
	for _, d := range rules.Descriptions {
		all = append(all, struct{ ID, Severity string }{d.ID, d.Severity})
	}
	for _, d := range karpenter.Rules {
		all = append(all, struct{ ID, Severity string }{d.ID, d.Severity})
	}
	if len(Rules) != len(all) {
		t.Fatalf("%d explanations for %d rules", len(Rules), len(all))
	}
	for _, d := range all {
		r, ok := Find(d.ID)
		if !ok {
			t.Errorf("%s has no explanation", d.ID)
			continue
		}
		if r.Severity != d.Severity {
			t.Errorf("%s: explained as %s, reported as %s", d.ID, r.Severity, d.Severity)
		}
		var buf bytes.Buffer
		WriteRule(&buf, r)
		for _, want := range []string{r.ID + ": ", "Why it matters", "Detected when", "Recommended configuration", "References\n  https://"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("%s: output is missing %q", d.ID, want)
			}
		}
		if r.Why == "" || r.Detected == "" || r.Good == "" {
			t.Errorf("%s has an empty section", d.ID)
		}
	}
}

func TestWorkloadBreakdown(t *testing.T) {
	res := rules.Result{Kind: "Deployment", Namespace: "shop", Name: "api", Score: 65, Verdict: rules.Caveats, Confidence: "medium",
		ConfidenceReasons: []string{"job: whether it checkpoints its progress cannot be verified"},
		Findings: []rules.Finding{
			{Rule: "SR004", Severity: rules.Caveat, Message: "no spread"},
			{Rule: "SR005", Severity: rules.Caveat, Message: "no PDB"},
			{Rule: "SR009", Severity: rules.Info, Message: "no preStop"},
		},
		Ignored:     []rules.Ignored{{Rule: "SR007", Reason: "stateful on purpose"}},
		Flexibility: rules.Flexibility{Score: 85, Zones: 2, Families: []string{"c", "m"}, Constraints: "low"}}
	var buf bytes.Buffer
	WriteWorkload(&buf, res)
	for _, want := range []string{"shop/deployment/api", "100  starting score", "-15  SR004 (caveat)  no spread", "-5  SR009 (info)", "65  final score",
		"Verdict: ready-with-caveats", "there are caveats (SR004, SR005) but no blocker", "Confidence: medium", "checkpoints",
		"SR007: stateful on purpose", "Spot flexibility: 85", "2 zone(s)", "families c, m"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q\n%s", want, buf.String())
		}
	}
}

func TestVerdictReasons(t *testing.T) {
	cases := []struct {
		verdict  rules.Verdict
		findings []rules.Finding
		want     string
	}{
		{rules.NotReady, []rules.Finding{{Rule: "SR001", Severity: rules.Blocker}}, "a blocker makes the workload not ready"},
		{rules.NotReady, []rules.Finding{{Rule: "SR003", Severity: rules.Caveat}, {Rule: "SR004", Severity: rules.Caveat}}, "took the score below 50"},
		{rules.Ready, []rules.Finding{{Rule: "SR009", Severity: rules.Info}}, "no blocker and no caveat"},
	}
	for _, c := range cases {
		if got := verdictReason(rules.Result{Verdict: c.verdict, Findings: c.findings}); !strings.Contains(got, c.want) {
			t.Errorf("verdictReason = %q, want one containing %q", got, c.want)
		}
	}
}
