// Command spot-readiness tells which Kubernetes workloads can safely run on Spot capacity.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/robertasolimandonofreo/spot-readiness/internal/cluster"
	"github.com/robertasolimandonofreo/spot-readiness/internal/config"
	"github.com/robertasolimandonofreo/spot-readiness/internal/interruption"
	"github.com/robertasolimandonofreo/spot-readiness/internal/karpenter"
	"github.com/robertasolimandonofreo/spot-readiness/internal/load"
	"github.com/robertasolimandonofreo/spot-readiness/internal/report"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

var version = "dev"

const usage = `spot-readiness %s

Usage:
  spot-readiness scan [flags] <path>...   scan YAML/JSON files or directories ("-" reads stdin)
  spot-readiness scan [flags]             no paths: read the current cluster (read-only)
  spot-readiness version

Flags for scan:
  -f, --format string     table (default), json, csv, markdown or sarif (-o/--output also work)
  -v, --verbose           explain every finding
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "--version":
		fmt.Println(version)
	case "scan":
		os.Exit(scan(os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
}

func scan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	var format, failOn, kubeContext, kubeconfig, namespace, configPath, baselinePath string
	var verbose, includeSystem, withKarpenter, withInterruption, failOnRegression bool
	for _, name := range []string{"f", "format", "o", "output"} {
		fs.StringVar(&format, name, "table", "output format: table, json, csv, markdown or sarif")
	}
	fs.BoolVar(&verbose, "v", false, "explain findings")
	fs.BoolVar(&verbose, "verbose", false, "explain findings")
	fs.BoolVar(&includeSystem, "include-system", false, "include system namespaces")
	fs.BoolVar(&withKarpenter, "karpenter", false, "check Karpenter NodePools")
	fs.BoolVar(&withInterruption, "interruption", false, "assess interruption handling")
	fs.StringVar(&baselinePath, "baseline", "", "baseline JSON report")
	fs.BoolVar(&failOnRegression, "fail-on-regression", false, "fail on new findings")
	fs.StringVar(&configPath, "config", "", "config file")
	fs.StringVar(&failOn, "fail-on", "", "not-ready or caveats")
	fs.StringVar(&kubeContext, "context", "", "kubeconfig context")
	fs.StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig file")
	fs.StringVar(&namespace, "n", "", "namespace")
	fs.StringVar(&namespace, "namespace", "", "namespace")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	clusterMode := fs.NArg() == 0
	if !clusterMode && (kubeContext != "" || kubeconfig != "" || namespace != "") {
		fmt.Fprintln(os.Stderr, "--context, --kubeconfig and --namespace only apply when reading a cluster (no paths)")
		return 2
	}
	if format == "md" {
		format = "markdown"
	}
	if format != "table" && format != "json" && format != "csv" && format != "markdown" && format != "sarif" {
		fmt.Fprintf(os.Stderr, "unknown format %q (use table, json, csv, markdown or sarif)\n", format)
		return 2
	}
	if failOn != "" && failOn != "not-ready" && failOn != "caveats" {
		fmt.Fprintf(os.Stderr, "unknown --fail-on %q (use not-ready or caveats)\n", failOn)
		return 2
	}

	cfg := &config.Config{}
	if configPath == "" {
		configPath = config.Find()
	}
	if configPath != "" {
		loaded, err := config.Load(configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", configPath, err)
			return 2
		}
		cfg = loaded
		fmt.Fprintf(os.Stderr, "using config %s\n", configPath)
	}
	// The flag wins over the file, so one run can be stricter or looser than the team default.
	if failOn == "" {
		failOn = cfg.Thresholds.FailOn
	}

	if failOnRegression && baselinePath == "" {
		fmt.Fprintln(os.Stderr, "--fail-on-regression needs --baseline")
		return 2
	}
	var baseline report.Report
	if baselinePath != "" {
		b, err := report.LoadBaseline(baselinePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
		baseline = b
	}

	var inv *rules.Inventory
	var err error
	if clusterMode {
		inv, err = scanCluster(kubeconfig, kubeContext, namespace, withKarpenter)
	} else {
		inv, err = load.Paths(fs.Args())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	results := rules.AnalyzeWith(inv, cfg.Options(includeSystem))
	rep := report.Build(results, inv.Skipped)
	if baselinePath != "" {
		rep.Comparison = report.Compare(baseline.Results, results)
	}
	// The assessment looks for Karpenter and the Node Termination Handler, which only a whole
	// cluster shows. It is on in cluster mode (unless one namespace was selected) and, for files,
	// only on request, because a repository rarely holds those components.
	if (clusterMode && namespace == "") || withInterruption {
		if namespace != "" {
			fmt.Fprintln(os.Stderr, "note: interruption handling needs every namespace, skipped")
		} else {
			a := interruption.Assess(inv, results)
			rep.Interruption = &a
		}
	}
	if withKarpenter {
		rep.Karpenter = karpenter.AnalyzeWith(&inv.Karpenter, cfg.KarpenterOptions())
		if rep.Karpenter == nil {
			rep.Karpenter = []karpenter.Result{}
		}
	}
	switch format {
	case "json":
		err = report.JSON(os.Stdout, rep)
	case "csv":
		err = report.CSV(os.Stdout, rep)
	case "markdown":
		report.Markdown(os.Stdout, rep)
	case "sarif":
		err = report.SARIF(os.Stdout, rep, version)
	default:
		report.Table(os.Stdout, rep, verbose)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	if failOnRegression && rep.Comparison.Regressions() > 0 {
		fmt.Fprintf(os.Stderr, "%d new blocker or caveat finding(s) compared with the baseline\n", rep.Comparison.Regressions())
		return 1
	}
	below := 0
	for _, r := range results {
		if r.Score < cfg.Thresholds.MinimumScore {
			below++
		}
	}
	if below > 0 {
		fmt.Fprintf(os.Stderr, "%d workload(s) score below the configured minimum of %d\n", below, cfg.Thresholds.MinimumScore)
		return 1
	}
	if (failOn == "not-ready" && rep.Summary.NotReady > 0) ||
		(failOn == "caveats" && rep.Summary.NotReady+rep.Summary.Caveats > 0) {
		return 1
	}
	return 0
}

func scanCluster(kubeconfig, kubeContext, namespace string, withKarpenter bool) (*rules.Inventory, error) {
	clients, name, err := cluster.Connect(cluster.Options{Kubeconfig: kubeconfig, Context: kubeContext, Namespace: namespace})
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "reading cluster %q (read-only)\n", name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	inv, err := cluster.Load(ctx, clients, namespace)
	if err == nil && withKarpenter {
		cluster.LoadKarpenter(ctx, clients, inv)
	}
	return inv, err
}
