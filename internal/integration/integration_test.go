//go:build integration

// Package integration runs the command against a real cluster (kind) with a set of fixtures, which
// exercises what unit tests with a fake client cannot: the real API server, the real list calls and
// custom resource definitions, the owner references of the pods and the labels of the node.
//
//	kind create cluster --name spot-readiness-it
//	SPOT_READINESS_CONTEXT=kind-spot-readiness-it go test -tags integration ./internal/integration
//
// It applies objects, so it only runs against a context whose name starts with "kind-".
package integration

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertasolimandonofreo/spot-readiness/internal/app"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func kubectl(t *testing.T, context string, args ...string) string {
	t.Helper()
	cmd := exec.Command("kubectl", append([]string{"--context", context}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestCluster(t *testing.T) {
	context := os.Getenv("SPOT_READINESS_CONTEXT")
	if context == "" {
		t.Skip("set SPOT_READINESS_CONTEXT to a kind context to run the integration test")
	}
	if !strings.HasPrefix(context, "kind-") {
		t.Fatalf("refusing to apply fixtures to %q: the context must be a kind cluster (kind-...)", context)
	}

	// The CRDs first, and wait until the API serves them.
	kubectl(t, context, "apply", "-f", "testdata/crds.yaml")
	kubectl(t, context, "wait", "--for=condition=Established", "--timeout=60s", "crd/nodepools.karpenter.sh", "crd/nodeclaims.karpenter.sh",
		"crd/ec2nodeclasses.karpenter.k8s.aws", "crd/scaledobjects.keda.sh", "crd/scaledjobs.keda.sh", "crd/rollouts.argoproj.io")
	kubectl(t, context, "apply", "-f", "testdata/workloads.yaml")
	kubectl(t, context, "apply", "-f", "testdata/karpenter.yaml")
	// The node looks like a Spot node launched by Karpenter, in one zone.
	kubectl(t, context, "label", "nodes", "--all", "--overwrite", "karpenter.sh/capacity-type=spot", "karpenter.sh/nodepool=spot-pool",
		"topology.kubernetes.io/zone=us-east-1a", "node.kubernetes.io/instance-type=m5.large")
	kubectl(t, context, "wait", "--for=condition=Ready", "--timeout=240s", "pod", "--all", "-n", "shop")
	kubectl(t, context, "wait", "--for=condition=Ready", "--timeout=240s", "pod", "--all", "-n", "karpenter")

	cases := []struct {
		file string
		args []string
	}{
		{"scan.txt", []string{"scan", "--karpenter"}},
		{"scan-verbose.txt", []string{"scan", "-v", "-n", "shop"}},
		{"plan.txt", []string{"plan", "--karpenter"}},
		{"scan.json", []string{"scan", "-f", "json", "--karpenter"}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			var out, errOut bytes.Buffer
			args := append(c.args, "--context", context, "--config", "testdata/config.yaml")
			if code := app.Run(args, app.Env{Stdout: &out, Stderr: &errOut, Version: "v9.9.9"}); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			path := filepath.Join("testdata", "golden", c.file)
			if *update {
				if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if out.String() != string(want) {
				t.Errorf("%s differs from the golden file; run with -update and read the diff.\n--- got ---\n%s", c.file, out.String())
			}
		})
	}
}
