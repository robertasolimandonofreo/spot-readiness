// Command spot-readiness tells which Kubernetes workloads can safely run on Spot capacity.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/robertasolimandonofreo/spot-readiness/internal/load"
	"github.com/robertasolimandonofreo/spot-readiness/internal/report"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

var version = "dev"

const usage = `spot-readiness %s

Usage:
  spot-readiness scan [flags] <path>...   scan YAML/JSON files or directories ("-" reads stdin)
  spot-readiness version

Flags for scan:
  -o, --output string     table (default) or json
  -v, --verbose           explain every finding
      --include-system    also analyze kube-system, kube-public and kube-node-lease
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
	var output, failOn string
	var verbose, includeSystem bool
	fs.StringVar(&output, "o", "table", "output format")
	fs.StringVar(&output, "output", "table", "output format")
	fs.BoolVar(&verbose, "v", false, "explain findings")
	fs.BoolVar(&verbose, "verbose", false, "explain findings")
	fs.BoolVar(&includeSystem, "include-system", false, "include system namespaces")
	fs.StringVar(&failOn, "fail-on", "", "not-ready or caveats")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "scan needs at least one path (use - for stdin)")
		return 2
	}
	if output != "table" && output != "json" {
		fmt.Fprintf(os.Stderr, "unknown output %q (use table or json)\n", output)
		return 2
	}
	if failOn != "" && failOn != "not-ready" && failOn != "caveats" {
		fmt.Fprintf(os.Stderr, "unknown --fail-on %q (use not-ready or caveats)\n", failOn)
		return 2
	}

	inv, err := load.Paths(fs.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	rep := report.Build(rules.Analyze(inv, includeSystem), inv.Skipped)
	if output == "json" {
		err = report.JSON(os.Stdout, rep)
	} else {
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
