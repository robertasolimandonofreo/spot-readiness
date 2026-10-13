// Package config reads .spot-readiness.yaml, which lets a team adopt the tool without accepting
// every heuristic: it can exclude namespaces, change a rule's severity, ignore a finding for one
// workload (with a reason) and set the thresholds that fail a build.
package config

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/robertasolimandonofreo/spot-readiness/internal/karpenter"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

// DefaultFiles are looked up in the current directory when --config is not given.
var DefaultFiles = []string{".spot-readiness.yaml", ".spot-readiness.yml"}

type Config struct {
	Version    int `json:"version"`
	Thresholds struct {
		// MinimumScore fails the scan when a workload scores below it.
		MinimumScore int `json:"minimumScore"`
		// FailOn is "not-ready" or "caveats", like the --fail-on flag.
		FailOn string `json:"failOn"`
	} `json:"thresholds"`
	Exclude struct {
		Namespaces []string `json:"namespaces"`
	} `json:"exclude"`
	Rules  map[string]RuleConfig `json:"rules"`
	Ignore []IgnoreEntry         `json:"ignore"`
	Spot   struct {
		MinZones    int   `json:"minZones"`
		MinReplicas int32 `json:"minReplicas"`
	} `json:"spot"`
}

type RuleConfig struct {
	// Severity is "blocker", "caveat", "info" or "off".
	Severity string `json:"severity"`
}

type IgnoreEntry struct {
	// Workload is "namespace/name"; "*" matches any characters within one part ("shop/*").
	Workload string   `json:"workload"`
	Kind     string   `json:"kind"`
	Rules    []string `json:"rules"`
	Reason   string   `json:"reason"`
}

// Find returns the path of the config file in the current directory, or "".
func Find() string {
	for _, name := range DefaultFiles {
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}
	return ""
}

// Load reads and validates a config file. Unknown fields are errors, so a typo does not silently
// disable a setting.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func knownRules() map[string]bool {
	out := map[string]bool{}
	for _, d := range rules.Descriptions {
		out[d.ID] = true
	}
	for _, d := range karpenter.Rules {
		out[d.ID] = true
	}
	return out
}

// Parse reads and validates a config document.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if c.Version != 1 {
		return nil, fmt.Errorf("invalid config: version must be 1, got %d", c.Version)
	}
	if f := c.Thresholds.FailOn; f != "" && f != "not-ready" && f != "caveats" {
		return nil, fmt.Errorf("invalid config: thresholds.failOn must be not-ready or caveats, got %q", f)
	}
	if s := c.Thresholds.MinimumScore; s < 0 || s > 100 {
		return nil, fmt.Errorf("invalid config: thresholds.minimumScore must be between 0 and 100, got %d", s)
	}
	if c.Spot.MinZones < 0 || c.Spot.MinReplicas < 0 {
		return nil, fmt.Errorf("invalid config: spot.minZones and spot.minReplicas cannot be negative")
	}
	known := knownRules()
	for id, r := range c.Rules {
		if !known[id] {
			return nil, fmt.Errorf("invalid config: rules: unknown rule %q", id)
		}
		switch r.Severity {
		case "blocker", "caveat", "info", "off":
		default:
			return nil, fmt.Errorf("invalid config: rules.%s.severity must be blocker, caveat, info or off, got %q", id, r.Severity)
		}
	}
	for i, ig := range c.Ignore {
		if ig.Workload == "" || strings.Count(ig.Workload, "/") != 1 {
			return nil, fmt.Errorf("invalid config: ignore[%d].workload must be namespace/name, got %q", i, ig.Workload)
		}
		for _, id := range ig.Rules {
			if !known[id] {
				return nil, fmt.Errorf("invalid config: ignore[%d]: unknown rule %q", i, id)
			}
		}
	}
	return &c, nil
}

// Options turns the configuration into analysis options. includeSystem is the --include-system flag.
func (c *Config) Options(includeSystem bool) rules.Options {
	o := rules.Options{IncludeSystem: includeSystem, ExcludeNamespaces: c.Exclude.Namespaces,
		MinReplicas: c.Spot.MinReplicas, MinZones: c.Spot.MinZones, Severity: map[string]rules.Severity{}}
	for id, r := range c.Rules {
		o.Severity[id] = rules.Severity(r.Severity)
	}
	for _, ig := range c.Ignore {
		o.Ignore = append(o.Ignore, rules.Ignore{Workload: ig.Workload, Kind: ig.Kind, Rules: ig.Rules, Reason: ig.Reason})
	}
	return o
}

// KarpenterOptions are the options for the NodePool analysis.
func (c *Config) KarpenterOptions() karpenter.Options {
	o := karpenter.Options{MinZones: c.Spot.MinZones, Severity: map[string]string{}}
	for id, r := range c.Rules {
		o.Severity[id] = r.Severity
	}
	return o
}
