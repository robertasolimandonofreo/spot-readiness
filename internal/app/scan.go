package app

import (
	"fmt"

	"github.com/robertasolimandonofreo/spot-readiness/internal/report"
)

func runScan(args []string, env Env) int {
	fs, o := newFlags("scan", env)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !o.validate(fs.Args(), env) {
		return 2
	}
	switch o.format {
	case "table", "json", "csv", "markdown", "sarif":
	default:
		fmt.Fprintf(env.Stderr, "unknown format %q (use table, json, csv, markdown or sarif)\n", o.format)
		return 2
	}
	a, err := analyze(o, fs.Args(), env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return 2
	}

	switch o.format {
	case "json":
		err = report.JSON(env.Stdout, a.rep)
	case "csv":
		err = report.CSV(env.Stdout, a.rep)
	case "markdown":
		report.Markdown(env.Stdout, a.rep)
	case "sarif":
		err = report.SARIF(env.Stdout, a.rep, env.Version)
	default:
		report.TableWith(env.Stdout, a.rep, report.TableOptions{Verbose: o.verbose, Suggest: o.suggest})
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return 2
	}
	return exitCode(o, a, env)
}

// exitCode applies the thresholds: it is 1 when the scan found a regression, a workload below the
// minimum score or, with --fail-on, a verdict that is not good enough.
func exitCode(o *options, a *analysis, env Env) int {
	if o.failOnRegression && a.rep.Comparison.Regressions() > 0 {
		fmt.Fprintf(env.Stderr, "%d new blocker or caveat finding(s) compared with the baseline\n", a.rep.Comparison.Regressions())
		return 1
	}
	below := 0
	for _, r := range a.results {
		if r.Score < a.cfg.Thresholds.MinimumScore {
			below++
		}
	}
	if below > 0 {
		fmt.Fprintf(env.Stderr, "%d workload(s) score below the configured minimum of %d\n", below, a.cfg.Thresholds.MinimumScore)
		return 1
	}
	s := a.rep.Summary
	if (o.failOn == "not-ready" && s.NotReady > 0) || (o.failOn == "caveats" && s.NotReady+s.Caveats > 0) {
		return 1
	}
	return 0
}
