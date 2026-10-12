// Command spot-readiness tells which Kubernetes workloads can safely run on Spot capacity.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/robertasolimandonofreo/spot-readiness/internal/cluster"
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
  -f, --format string     table (default), json, csv or markdown (-o/--output also work)
  -v, --verbose           explain every finding
      --include-system    also analyze kube-system, kube-public and kube-node-lease
      --context string    kubeconfig context to read (cluster mode)
      --kubeconfig string kubeconfig file (cluster mode; default KUBECONFIG or ~/.kube/config)
  -n, --namespace string  only this namespace (cluster mode; default all)
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
	var format, failOn, kubeContext, kubeconfig, namespace string
	var verbose, includeSystem bool
	for _, name := range []string{"f", "format", "o", "output"} {
		fs.StringVar(&format, name, "table", "output format: table, json, csv or markdown")
	}
	fs.BoolVar(&verbose, "v", false, "explain findings")
	fs.BoolVar(&verbose, "verbose", false, "explain findings")
	fs.BoolVar(&includeSystem, "include-system", false, "include system namespaces")
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
	if format != "table" && format != "json" && format != "csv" && format != "markdown" {
		fmt.Fprintf(os.Stderr, "unknown format %q (use table, json, csv or markdown)\n", format)
		return 2
	}
	if failOn != "" && failOn != "not-ready" && failOn != "caveats" {
		fmt.Fprintf(os.Stderr, "unknown --fail-on %q (use not-ready or caveats)\n", failOn)
		return 2
	}

	var inv *rules.Inventory
	var err error
	if clusterMode {
		inv, err = scanCluster(kubeconfig, kubeContext, namespace)
	} else {
		inv, err = load.Paths(fs.Args())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	rep := report.Build(rules.Analyze(inv, includeSystem), inv.Skipped)
	switch format {
	case "json":
		err = report.JSON(os.Stdout, rep)
	case "csv":
		err = report.CSV(os.Stdout, rep)
	case "markdown":
		report.Markdown(os.Stdout, rep)
	default:
		report.Table(os.Stdout, rep, verbose)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	if (failOn == "not-ready" && rep.Summary.NotReady > 0) ||
		(failOn == "caveats" && rep.Summary.NotReady+rep.Summary.Caveats > 0) {
		return 1
	}
	return 0
}

func scanCluster(kubeconfig, kubeContext, namespace string) (*rules.Inventory, error) {
	client, name, err := cluster.Connect(cluster.Options{Kubeconfig: kubeconfig, Context: kubeContext, Namespace: namespace})
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "reading cluster %q (read-only)\n", name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return cluster.Load(ctx, client, namespace)
}
