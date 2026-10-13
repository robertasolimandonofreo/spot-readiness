package app

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in internal/app/testdata/golden")

// The golden files are the exact output of the command on testdata. A change in a table, a report
// or an API shows here as a diff: run "go test ./internal/app -update" to accept it, and read the diff.
func TestGolden(t *testing.T) {
	cases := []struct {
		file string
		args []string
	}{
		{"scan.txt", []string{"scan", "testdata"}},
		{"scan-verbose.txt", []string{"scan", "-v", "--suggest", "testdata"}},
		{"scan-karpenter.txt", []string{"scan", "--karpenter", "testdata"}},
		{"scan.json", []string{"scan", "-f", "json", "--karpenter", "testdata"}},
		{"scan.md", []string{"scan", "-f", "markdown", "--karpenter", "testdata"}},
		{"scan.csv", []string{"scan", "-f", "csv", "testdata"}},
		{"scan.sarif", []string{"scan", "-f", "sarif", "testdata"}},
		{"plan.txt", []string{"plan", "--karpenter", "testdata"}},
		{"plan.json", []string{"plan", "-f", "json", "--karpenter", "testdata"}},
		{"plan.md", []string{"plan", "-f", "markdown", "testdata"}},
		{"explain-rule.txt", []string{"explain", "SR014"}},
		{"explain-workload.txt", []string{"explain", "shop/worker", "testdata"}},
	}
	root := repoRoot(t)
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			code, out, errOut := run(t, c.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			path := filepath.Join(root, "internal", "app", "testdata", "golden", c.file)
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/app -update to create it)", err)
			}
			if out != string(want) {
				t.Errorf("%s differs from the golden file; run go test ./internal/app -update and read the diff.\n--- got ---\n%s", c.file, out)
			}
			// The same command twice must give the same bytes, or the golden file would flicker.
			if _, again, _ := run(t, c.args...); again != out {
				t.Errorf("%s is not deterministic", c.file)
			}
		})
	}
}
