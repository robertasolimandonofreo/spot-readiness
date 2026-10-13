package app

import (
	"encoding/json"
	"fmt"

	"github.com/robertasolimandonofreo/spot-readiness/internal/interruption"
	"github.com/robertasolimandonofreo/spot-readiness/internal/plan"
)

func runPlan(args []string, env Env) int {
	fs, o := newFlags("plan", env)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !o.validate(fs.Args(), env) {
		return 2
	}
	if o.format != "table" && o.format != "json" && o.format != "markdown" {
		fmt.Fprintf(env.Stderr, "unknown format %q for plan (use table, json or markdown)\n", o.format)
		return 2
	}
	if o.baselinePath != "" || o.failOn != "" || o.failOnRegression {
		fmt.Fprintln(env.Stderr, "--baseline, --fail-on and --fail-on-regression apply to scan, not plan")
		return 2
	}
	a, err := analyze(o, fs.Args(), env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return 2
	}

	var prerequisites []string
	if a.rep.Interruption != nil {
		prerequisites = append(prerequisites, interruption.Prerequisites(a.inv)...)
	}
	for _, np := range a.rep.Karpenter {
		for _, f := range np.Findings {
			if f.Severity != "info" {
				prerequisites = append(prerequisites, fmt.Sprintf("NodePool %s (%s): %s", np.NodePool, f.Rule, f.Message))
			}
		}
	}
	p := plan.Build(a.results, prerequisites)
	p.ToolVersion = env.Version
	switch o.format {
	case "json":
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(p)
	case "markdown":
		plan.Markdown(env.Stdout, p)
	default:
		plan.Table(env.Stdout, p, plan.Options{Suggest: o.suggest})
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return 2
	}
	return 0
}
