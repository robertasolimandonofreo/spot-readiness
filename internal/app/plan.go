package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/robertasolimandonofreo/spot-readiness/internal/interruption"
	"github.com/robertasolimandonofreo/spot-readiness/internal/plan"
	"github.com/robertasolimandonofreo/spot-readiness/internal/pricing"
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
	provider, err := o.priceProvider()
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
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
	if provider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		rates, err := provider.Rates(ctx)
		if err != nil {
			fmt.Fprintln(env.Stderr, "error:", err)
			return 2
		}
		s := p.Summary
		est := pricing.Compute(rates, s.CPUMilli, s.ImmediateCPUMilli, s.PotentialCPUMilli)
		p.Cost = &est
	}
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

func (o *options) hasPricingFlags() bool {
	return o.pricing != "" || o.priceFile != "" || o.onDemandPrice != 0 || o.spotDiscount != 0 || o.currency != "" || o.region != "" || o.instanceType != ""
}

// priceProvider builds the provider chosen with --pricing, or nil when no estimate was asked for.
func (o *options) priceProvider() (pricing.Provider, error) {
	if o.pricing == "" {
		if o.hasPricingFlags() {
			return nil, fmt.Errorf("the price flags need --pricing static or --pricing aws")
		}
		return nil, nil
	}
	switch o.pricing {
	case "static":
		if o.region != "" || o.instanceType != "" {
			return nil, fmt.Errorf("--region and --instance-type are for --pricing aws")
		}
		if o.priceFile != "" {
			if o.onDemandPrice != 0 || o.spotDiscount != 0 {
				return nil, fmt.Errorf("give the prices in --price-file or with --on-demand-vcpu-hour and --spot-discount, not both")
			}
			return pricing.StaticFromFile(o.priceFile)
		}
		if o.onDemandPrice == 0 {
			return nil, fmt.Errorf("--pricing static needs --price-file, or --on-demand-vcpu-hour and --spot-discount: the tool has no prices of its own")
		}
		return pricing.NewStatic(o.onDemandPrice, o.spotDiscount, o.currency)
	case "aws":
		if o.priceFile != "" || o.onDemandPrice != 0 || o.spotDiscount != 0 || o.currency != "" {
			return nil, fmt.Errorf("the price flags are for --pricing static")
		}
		return pricing.AWS{Region: o.region, InstanceType: o.instanceType}, nil
	}
	return nil, fmt.Errorf("unknown --pricing %q (use static or aws)", o.pricing)
}
