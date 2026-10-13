package report

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func TestSARIF(t *testing.T) {
	rep := Build([]rules.Result{
		{Kind: "Deployment", Namespace: "shop", Name: "api", Score: 70, Verdict: rules.Caveats, Findings: []rules.Finding{
			{Rule: "SR006", Severity: rules.Caveat, Message: `container "api" has no readinessProbe`, File: "k8s/deployment.yaml", Line: 42},
			{Rule: "SR001", Severity: rules.Blocker, Message: "runs with 1 replica", File: "k8s/deployment.yaml"},
			{Rule: "SR009", Severity: rules.Info, Message: "no preStop hook"},
		}},
	}, 0)
	var buf bytes.Buffer
	if err := SARIF(&buf, rep, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	var log struct {
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name    string `json:"name"`
					Version string `json:"version"`
					Rules   []struct {
						ID string `json:"id"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID    string `json:"ruleId"`
				RuleIndex int    `json:"ruleIndex"`
				Level     string `json:"level"`
				Locations []struct {
					Physical *struct {
						Artifact struct{ URI string }    `json:"artifactLocation"`
						Region   struct{ StartLine int } `json:"region"`
					} `json:"physicalLocation"`
					Logical []struct {
						Name string `json:"fullyQualifiedName"`
					} `json:"logicalLocations"`
				} `json:"locations"`
				Fingerprints map[string]string `json:"partialFingerprints"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(buf.Bytes(), &log); err != nil {
		t.Fatal(err)
	}
	run := log.Runs[0]
	if log.Version != "2.1.0" || run.Tool.Driver.Name != "spot-readiness" || run.Tool.Driver.Version != "v1.2.3" || len(run.Results) != 3 {
		t.Fatalf("log: %s", buf.String())
	}
	r := run.Results[0]
	loc := r.Locations[0]
	if r.RuleID != "SR006" || r.Level != "warning" || loc.Physical.Artifact.URI != "k8s/deployment.yaml" || loc.Physical.Region.StartLine != 42 ||
		loc.Logical[0].Name != "shop/deployment/api" || run.Tool.Driver.Rules[r.RuleIndex].ID != "SR006" || r.Fingerprints["spotReadiness/v1"] == "" {
		t.Fatalf("result: %+v", r)
	}
	// A blocker without a line points to line 1, and a finding without a file has no physical location.
	if run.Results[1].Level != "error" || run.Results[1].Locations[0].Physical.Region.StartLine != 1 {
		t.Fatalf("blocker: %+v", run.Results[1])
	}
	if run.Results[2].Level != "note" || run.Results[2].Locations[0].Physical != nil {
		t.Fatalf("info: %+v", run.Results[2])
	}
	// The fingerprint does not depend on the line.
	if run.Results[0].Fingerprints["spotReadiness/v1"] == run.Results[1].Fingerprints["spotReadiness/v1"] {
		t.Fatal("different findings need different fingerprints")
	}
}

func TestSARIFArtifactURI(t *testing.T) {
	cases := map[string]sarifArtifact{
		"k8s/api.yaml":           {URI: "k8s/api.yaml", URIBaseID: "%SRCROOT%"},
		"/home/me/repo/api.yaml": {URI: "file:///home/me/repo/api.yaml"},
		"C:/repo/k8s/api.yaml":   {URI: "file:///C:/repo/k8s/api.yaml"},
		"../other/api.yaml":      {URI: "../other/api.yaml", URIBaseID: "%SRCROOT%"},
	}
	for file, want := range cases {
		if got := artifact(file); got != want {
			t.Errorf("artifact(%q) = %+v, want %+v", file, got, want)
		}
	}
}
