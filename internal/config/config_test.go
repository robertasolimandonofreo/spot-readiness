package config

import (
	"strings"
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

const example = `
version: 1
thresholds:
  minimumScore: 70
  failOn: not-ready
exclude:
  namespaces: [monitoring]
rules:
  SR007:
    severity: info
  SR009:
    severity: "off"
ignore:
  - workload: database/postgres
    rules: [SR007]
    reason: Stateful workload intentionally kept on on-demand
spot:
  minZones: 2
  minReplicas: 3
`

func TestParse(t *testing.T) {
	c, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	if c.Thresholds.MinimumScore != 70 || c.Thresholds.FailOn != "not-ready" || c.Spot.MinReplicas != 3 {
		t.Fatalf("%+v", c)
	}
	o := c.Options(true)
	if !o.IncludeSystem || o.MinReplicas != 3 || o.Severity["SR007"] != rules.Info || o.Severity["SR009"] != rules.SeverityOff ||
		len(o.Ignore) != 1 || o.Ignore[0].Reason == "" || o.ExcludeNamespaces[0] != "monitoring" {
		t.Fatalf("options: %+v", o)
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string]string{
		"version: 2":                                        "version must be 1",
		"version: 1\nthreshold: {}":                         "unknown field",
		"version: 1\nthresholds: {failOn: never}":           "failOn",
		"version: 1\nthresholds: {minimumScore: 120}":       "minimumScore",
		"version: 1\nrules: {SR999: {severity: info}}":      "unknown rule",
		"version: 1\nrules: {SR007: {severity: critical}}":  "severity",
		"version: 1\nignore: [{workload: postgres}]":        "namespace/name",
		"version: 1\nignore: [{workload: a/b, rules: [X]}]": "unknown rule",
		"version: 1\nspot: {minZones: -1}":                  "negative",
	}
	for doc, want := range cases {
		if _, err := Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want one containing %q", doc, err, want)
		}
	}
}
