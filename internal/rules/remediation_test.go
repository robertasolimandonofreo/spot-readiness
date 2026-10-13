package rules

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func TestEveryRuleHasADocsLink(t *testing.T) {
	for _, d := range Descriptions {
		if !strings.HasPrefix(DocsURL(d.ID), "https://") {
			t.Errorf("%s has no docs link", d.ID)
		}
	}
}

// The snippet after the advice must be valid YAML, so it can be pasted into a manifest.
func TestRemediationSnippetsAreYAML(t *testing.T) {
	w := healthy()
	inv := inventoryWithPDB()
	for _, d := range Descriptions {
		f := &Finding{Rule: d.ID, Path: "pod.containers[web]"}
		remediate(f, inv, w)
		if f.Remediation == "" || f.DocsURL == "" {
			t.Errorf("%s: no remediation or docs link", d.ID)
			continue
		}
		_, snippet, ok := strings.Cut(f.Remediation, "\n\n")
		if !ok {
			continue // advice only
		}
		var body []string
		for _, line := range strings.Split(snippet, "\n") {
			if !strings.HasPrefix(line, "# ") {
				body = append(body, line)
			}
		}
		var out any
		if err := yaml.Unmarshal([]byte(strings.Join(body, "\n")), &out); err != nil {
			t.Errorf("%s: snippet is not YAML: %v\n%s", d.ID, err, snippet)
		}
	}
}

func TestRemediationUsesTheWorkloadsNames(t *testing.T) {
	w := healthy()
	w.Template.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 9000}}
	f := &Finding{Rule: "SR006", Path: "pod.containers[web]"}
	remediate(f, inventoryWithPDB(), w)
	if !strings.Contains(f.Remediation, "port: 9000") || !strings.Contains(f.Remediation, "spec.template.spec.containers[web]") {
		t.Fatalf("SR006: %s", f.Remediation)
	}
	f = &Finding{Rule: "SR004"}
	remediate(f, inventoryWithPDB(), w)
	if !strings.Contains(f.Remediation, "app: web") {
		t.Fatalf("SR004: %s", f.Remediation)
	}
	f = &Finding{Rule: "SR005"}
	remediate(f, inventoryWithPDB(), w)
	if !strings.Contains(f.Remediation, "namespace: shop") || !strings.Contains(f.Remediation, "name: web") {
		t.Fatalf("SR005: %s", f.Remediation)
	}
}

func TestFindingsCarryRemediation(t *testing.T) {
	w := healthy()
	w.Template.Spec.Containers[0].ReadinessProbe = nil
	r := Evaluate(inventoryWithPDB(), w)
	if len(r.Findings) != 1 || r.Findings[0].Remediation == "" || r.Findings[0].DocsURL == "" {
		t.Fatalf("findings: %+v", r.Findings)
	}
}
