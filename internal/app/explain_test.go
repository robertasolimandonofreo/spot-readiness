package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot finds the directory with go.mod, going up from the test's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// run executes the command in memory from the repository root, so the files in testdata are
// reported with the same paths on every machine.
func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Chdir(repoRoot(t))
	var out, errOut bytes.Buffer
	code = Run(args, Env{Stdout: &out, Stderr: &errOut, Version: "v9.9.9"})
	return code, out.String(), errOut.String()
}

func TestExplainRule(t *testing.T) {
	code, out, _ := run(t, "explain", "sr014")
	if code != 0 || !strings.Contains(out, "SR014: PodDisruptionBudget allows zero voluntary disruptions") || !strings.Contains(out, "Recommended configuration") {
		t.Fatalf("code %d\n%s", code, out)
	}
	if code, _, errOut := run(t, "explain", "SR099"); code != 2 || !strings.Contains(errOut, "unknown rule") {
		t.Fatalf("unknown rule: %d %s", code, errOut)
	}
	if code, _, errOut := run(t, "explain"); code != 2 || !strings.Contains(errOut, "needs a rule") {
		t.Fatalf("no target: %d %s", code, errOut)
	}
}

func TestExplainWorkload(t *testing.T) {
	code, out, _ := run(t, "explain", "shop/worker", "testdata")
	if code != 0 || !strings.Contains(out, "shop/deployment/worker") || !strings.Contains(out, "35  final score") || !strings.Contains(out, "Verdict: not-ready") {
		t.Fatalf("code %d\n%s", code, out)
	}
	if code, out, _ := run(t, "explain", "shop/statefulset/db", "testdata"); code != 0 || !strings.Contains(out, "Confidence: low") {
		t.Fatalf("with the kind: %d\n%s", code, out)
	}
	if code, _, errOut := run(t, "explain", "shop/nothing", "testdata"); code != 2 || !strings.Contains(errOut, "no workload matches") {
		t.Fatalf("no match: %d %s", code, errOut)
	}
}

func TestPlanPricingFlags(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"plan", "--pricing", "static", "testdata"}, "has no prices of its own"},
		{[]string{"plan", "--pricing", "static", "--on-demand-vcpu-hour", "0.05", "--spot-discount", "1.5", "testdata"}, "Spot discount must be"},
		{[]string{"plan", "--pricing", "static", "--price-file", "x.yaml", "--on-demand-vcpu-hour", "0.05", "testdata"}, "not both"},
		{[]string{"plan", "--pricing", "static", "--region", "us-east-1", "--on-demand-vcpu-hour", "0.05", "testdata"}, "for --pricing aws"},
		{[]string{"plan", "--pricing", "aws", "--spot-discount", "0.5", "testdata"}, "for --pricing static"},
		{[]string{"plan", "--pricing", "gcp", "testdata"}, "unknown --pricing"},
		{[]string{"plan", "--spot-discount", "0.5", "testdata"}, "need --pricing"},
		{[]string{"scan", "--pricing", "static", "testdata"}, "apply to plan, not scan"},
		{[]string{"explain", "--pricing", "static", "shop/web", "testdata"}, "apply to plan, not explain"},
	}
	for _, c := range cases {
		code, _, errOut := run(t, c.args...)
		if code != 2 || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: code %d, stderr %q, want one containing %q", c.args, code, errOut, c.want)
		}
	}
	code, out, errOut := run(t, "plan", "--pricing", "static", "--on-demand-vcpu-hour", "0.048", "--spot-discount", "0.7", "testdata")
	if code != 0 || !strings.Contains(out, "ESTIMATED MONTHLY COMPUTE") || !strings.Contains(out, "Estimated savings") {
		t.Fatalf("code %d\n%s\n%s", code, out, errOut)
	}
	code, out, _ = run(t, "plan", "-f", "json", "--pricing", "static", "--on-demand-vcpu-hour", "0.048", "--spot-discount", "0.7", "testdata")
	if code != 0 || !strings.Contains(out, `"estimatedSavings"`) || !strings.Contains(out, `"disclaimer"`) {
		t.Fatalf("json: %d\n%s", code, out)
	}
}
