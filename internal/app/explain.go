package app

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/robertasolimandonofreo/spot-readiness/internal/explain"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

var ruleID = regexp.MustCompile(`(?i)^(SR|KR)[0-9]{3}$`)

// runExplain explains a rule ("explain SR014") or shows how a workload got its score
// ("explain shop/api ./k8s"). The flags come first, like in scan.
func runExplain(args []string, env Env) int {
	fs, o := newFlags("explain", env)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(env.Stderr, "explain needs a rule (SR001 to SR019, KR001 to KR007) or a workload (namespace/name)")
		return 2
	}
	target, paths := fs.Arg(0), fs.Args()[1:]
	if ruleID.MatchString(target) {
		r, ok := explain.Find(strings.ToUpper(target))
		if !ok {
			fmt.Fprintf(env.Stderr, "unknown rule %q\n", target)
			return 2
		}
		explain.WriteRule(env.Stdout, r)
		return 0
	}

	if !o.validate(paths, env) {
		return 2
	}
	a, err := analyze(o, paths, env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return 2
	}
	var found []rules.Result
	for _, r := range a.results {
		if matches(target, r) {
			found = append(found, r)
		}
	}
	if len(found) == 0 {
		fmt.Fprintf(env.Stderr, "no workload matches %q (use namespace/name, or namespace/kind/name; system namespaces need --include-system)\n", target)
		return 2
	}
	for i, r := range found {
		if i > 0 {
			fmt.Fprintln(env.Stdout, "\n----------------------------------------")
		}
		explain.WriteWorkload(env.Stdout, r)
	}
	return 0
}

// matches reports whether the target is namespace/name or namespace/kind/name of the workload.
func matches(target string, r rules.Result) bool {
	parts := strings.Split(target, "/")
	switch len(parts) {
	case 2:
		return parts[0] == r.Namespace && parts[1] == r.Name
	case 3:
		return parts[0] == r.Namespace && strings.EqualFold(parts[1], r.Kind) && parts[2] == r.Name
	}
	return false
}
