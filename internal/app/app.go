// Package app is the command line of spot-readiness: it parses the flags, reads the input, runs the
// analysis and writes the report. The command in cmd/spot-readiness only calls Run.
package app

import (
	"fmt"
	"io"
)

// Env is what Run needs from its surroundings, so tests can run the whole command in memory.
type Env struct {
	Stdout, Stderr io.Writer
	// Version is the release being run, printed by "version" and recorded in the reports.
	Version string
}

const usage = `spot-readiness %s

Spot readiness assessment for Kubernetes, with deep AWS EKS and Karpenter support.

Usage:
  spot-readiness scan [flags] <path>...   scan YAML/JSON files or directories ("-" reads stdin)
  spot-readiness scan [flags]             no paths: read the current cluster (read-only)
  spot-readiness plan [flags] [path]...   order the workloads into a Spot migration plan
  spot-readiness version

Flags for scan (plan takes the same input flags, -f table, json or markdown, --suggest and --karpenter;
it ignores --baseline and --fail-on):
  -f, --format string     table (default), json, csv, markdown or sarif (-o/--output also work)
  -v, --verbose           explain every finding
      --suggest           also print a suggested fix and a docs link for every finding
      --include-system    also analyze kube-system, kube-public and kube-node-lease
      --context string    kubeconfig context to read (cluster mode)
      --kubeconfig string kubeconfig file (cluster mode; default KUBECONFIG or ~/.kube/config)
  -n, --namespace string  only this namespace (cluster mode; default all)
      --karpenter         also check Karpenter NodePools (KR rules; cluster mode reads them, file mode reads them from the paths)
      --interruption      assess how ready the cluster is for Spot interruptions (on in cluster mode)
      --config string     config file (default .spot-readiness.yaml in the current directory, if present)
      --baseline string   JSON report (-f json) to compare with: shows new and resolved findings
      --fail-on-regression exit 1 when a blocker or caveat finding is new compared with --baseline
      --fail-on string    exit 1 when a workload is "not-ready" or "caveats" (default: never)
`

// Run runs the command with the arguments after the program name and returns the exit code: 0 on
// success, 1 when a threshold or --fail-on is not met, 2 on a usage or input error.
func Run(args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprintf(env.Stderr, usage, env.Version)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(env.Stdout, env.Version)
		return 0
	case "scan":
		return runScan(args[1:], env)
	case "plan":
		return runPlan(args[1:], env)
	case "-h", "--help", "help":
		fmt.Fprintf(env.Stdout, usage, env.Version)
		return 0
	}
	fmt.Fprintf(env.Stderr, "unknown command %q\n\n", args[0])
	fmt.Fprintf(env.Stderr, usage, env.Version)
	return 2
}
