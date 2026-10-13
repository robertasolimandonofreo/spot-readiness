package rules

import (
	"path"
	"strings"
)

// Options tunes the analysis. The zero value is the default behavior.
type Options struct {
	// IncludeSystem analyzes kube-system, kube-public and kube-node-lease too.
	IncludeSystem bool
	// ExcludeNamespaces are skipped entirely, even when IncludeSystem is set.
	ExcludeNamespaces []string
	// Severity overrides the severity of a rule by ID. SeverityOff drops the rule.
	Severity map[string]Severity
	// Ignore silences rules for the workloads it matches, keeping a record of why.
	Ignore []Ignore
	// MinReplicas is the replica count below which SR001 fires (default 2).
	MinReplicas int32
	// MinZones is the zone count below which SR016 fires (default 2).
	MinZones int
}

// SeverityOff, used as an override, turns a rule off.
const SeverityOff Severity = "off"

// Ignore silences rules for the workloads whose "namespace/name" matches Workload (a path.Match
// pattern, so "shop/*" works) and, when Kind is set, whose kind matches it case-insensitively. An
// empty Rules list silences every rule.
type Ignore struct {
	Workload string
	Kind     string
	Rules    []string
	Reason   string
}

// Ignored is a finding that an ignore entry silenced.
type Ignored struct {
	Rule   string `json:"rule"`
	Reason string `json:"reason,omitempty"`
}

func (o Options) minReplicas() int32 {
	if o.MinReplicas < 1 {
		return 2
	}
	return o.MinReplicas
}

func (o Options) minZones() int {
	if o.MinZones < 1 {
		return 2
	}
	return o.MinZones
}

func (o Options) excludes(namespace string) bool {
	for _, ns := range o.ExcludeNamespaces {
		if ns == namespace {
			return true
		}
	}
	return !o.IncludeSystem && (namespace == "kube-system" || namespace == "kube-public" || namespace == "kube-node-lease")
}

// ignoreFor returns the ignore entry that silences the rule for the workload, if any.
func (o Options) ignoreFor(w Workload, rule string) (Ignore, bool) {
	for _, ig := range o.Ignore {
		if ok, _ := path.Match(ig.Workload, w.Namespace+"/"+w.Name); !ok {
			continue
		}
		if ig.Kind != "" && !strings.EqualFold(ig.Kind, w.Kind) {
			continue
		}
		if len(ig.Rules) == 0 {
			return ig, true
		}
		for _, r := range ig.Rules {
			if r == rule {
				return ig, true
			}
		}
	}
	return Ignore{}, false
}
