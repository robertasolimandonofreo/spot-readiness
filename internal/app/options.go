package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/robertasolimandonofreo/spot-readiness/internal/cluster"
	"github.com/robertasolimandonofreo/spot-readiness/internal/config"
	"github.com/robertasolimandonofreo/spot-readiness/internal/interruption"
	"github.com/robertasolimandonofreo/spot-readiness/internal/karpenter"
	"github.com/robertasolimandonofreo/spot-readiness/internal/load"
	"github.com/robertasolimandonofreo/spot-readiness/internal/report"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

// options are the flags shared by scan, plan and explain.
type options struct {
	format, failOn, kubeContext, kubeconfig, namespace, configPath, baselinePath string
	verbose, includeSystem, karpenter, interruption, failOnRegression, suggest   bool
}

// newFlags returns a flag set that writes into the options.
func newFlags(name string, env Env) (*flag.FlagSet, *options) {
	o := &options{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	for _, n := range []string{"f", "format", "o", "output"} {
		fs.StringVar(&o.format, n, "table", "output format: table, json, csv, markdown or sarif")
	}
	fs.BoolVar(&o.verbose, "v", false, "explain findings")
	fs.BoolVar(&o.verbose, "verbose", false, "explain findings")
	fs.BoolVar(&o.suggest, "suggest", false, "print suggested fixes")
	fs.BoolVar(&o.includeSystem, "include-system", false, "include system namespaces")
	fs.BoolVar(&o.karpenter, "karpenter", false, "check Karpenter NodePools")
	fs.BoolVar(&o.interruption, "interruption", false, "assess interruption handling")
	fs.StringVar(&o.baselinePath, "baseline", "", "baseline JSON report")
	fs.BoolVar(&o.failOnRegression, "fail-on-regression", false, "fail on new findings")
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.failOn, "fail-on", "", "not-ready or caveats")
	fs.StringVar(&o.kubeContext, "context", "", "kubeconfig context")
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "kubeconfig file")
	fs.StringVar(&o.namespace, "n", "", "namespace")
	fs.StringVar(&o.namespace, "namespace", "", "namespace")
	return fs, o
}

// analysis is everything the commands work from: the configuration, the loaded objects, the results
// of the rules and the report built from them.
type analysis struct {
	cfg         *config.Config
	inv         *rules.Inventory
	results     []rules.Result
	rep         report.Report
	clusterMode bool
	baseline    report.Report
}

// validate checks the combinations of flags that do not depend on the command.
func (o *options) validate(paths []string, env Env) bool {
	if len(paths) > 0 && (o.kubeContext != "" || o.kubeconfig != "" || o.namespace != "") {
		fmt.Fprintln(env.Stderr, "--context, --kubeconfig and --namespace only apply when reading a cluster (no paths)")
		return false
	}
	if o.format == "md" {
		o.format = "markdown"
	}
	if o.failOn != "" && o.failOn != "not-ready" && o.failOn != "caveats" {
		fmt.Fprintf(env.Stderr, "unknown --fail-on %q (use not-ready or caveats)\n", o.failOn)
		return false
	}
	return true
}

// analyze reads the input (the paths, or the cluster when there are none) and runs the rules.
func analyze(o *options, paths []string, env Env) (*analysis, error) {
	a := &analysis{cfg: &config.Config{}, clusterMode: len(paths) == 0}
	if o.configPath == "" {
		o.configPath = config.Find()
	}
	if o.configPath != "" {
		loaded, err := config.Load(o.configPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", o.configPath, err)
		}
		a.cfg = loaded
		fmt.Fprintf(env.Stderr, "using config %s\n", o.configPath)
	}
	// The flag wins over the file, so one run can be stricter or looser than the team default.
	if o.failOn == "" {
		o.failOn = a.cfg.Thresholds.FailOn
	}
	if o.failOnRegression && o.baselinePath == "" {
		return nil, fmt.Errorf("--fail-on-regression needs --baseline")
	}
	if o.baselinePath != "" {
		b, err := report.LoadBaseline(o.baselinePath)
		if err != nil {
			return nil, err
		}
		a.baseline = b
	}

	var err error
	if a.clusterMode {
		a.inv, err = readCluster(o, env.Stderr)
	} else {
		a.inv, err = load.Paths(paths)
	}
	if err != nil {
		return nil, err
	}
	a.results = rules.AnalyzeWith(a.inv, a.cfg.Options(o.includeSystem))
	a.rep = report.Build(a.results, a.inv.Skipped)
	a.rep.ToolVersion = env.Version
	if o.baselinePath != "" {
		a.rep.Comparison = report.Compare(a.baseline.Results, a.results)
	}
	// The assessment looks for Karpenter and the Node Termination Handler, which only a whole
	// cluster shows. It is on in cluster mode (unless one namespace was selected) and, for files,
	// only on request, because a repository rarely holds those components.
	if (a.clusterMode && o.namespace == "") || o.interruption {
		if o.namespace != "" {
			fmt.Fprintln(env.Stderr, "note: interruption handling needs every namespace, skipped")
		} else {
			as := interruption.Assess(a.inv, a.results)
			a.rep.Interruption = &as
		}
	}
	if o.karpenter {
		a.rep.Karpenter = karpenter.AnalyzeWith(&a.inv.Karpenter, a.cfg.KarpenterOptions())
		if a.rep.Karpenter == nil {
			a.rep.Karpenter = []karpenter.Result{}
		}
	}
	return a, nil
}

func readCluster(o *options, stderr io.Writer) (*rules.Inventory, error) {
	clients, name, err := cluster.Connect(cluster.Options{Kubeconfig: o.kubeconfig, Context: o.kubeContext, Namespace: o.namespace})
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(stderr, "reading cluster %q (read-only)\n", name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	inv, err := cluster.Load(ctx, clients, o.namespace)
	if err == nil && o.karpenter {
		cluster.LoadKarpenter(ctx, clients, inv)
	}
	return inv, err
}
