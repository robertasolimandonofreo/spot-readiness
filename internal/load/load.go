// Package load reads Kubernetes manifests (files, directories or stdin) into an Inventory.
package load

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"sigs.k8s.io/yaml"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

var docSeparator = regexp.MustCompile(`(?m)^---\s*$`)

// Paths loads every .yaml, .yml and .json file under the given paths. "-" reads stdin.
func Paths(paths []string) (*rules.Inventory, error) {
	inv := &rules.Inventory{}
	for _, p := range paths {
		if p == "-" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return nil, err
			}
			if err := Add(inv, data); err != nil {
				return nil, fmt.Errorf("stdin: %w", err)
			}
			continue
		}
		err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".yaml", ".yml", ".json":
			default:
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := Add(inv, data); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return inv, nil
}

// Add parses a multi-document YAML (or JSON) stream into the inventory.
func Add(inv *rules.Inventory, data []byte) error {
	for _, doc := range docSeparator.Split(string(data), -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		jsonDoc, err := yaml.YAMLToJSON([]byte(doc))
		if err != nil {
			return err
		}
		if err := addJSON(inv, jsonDoc); err != nil {
			return err
		}
	}
	return nil
}

func addJSON(inv *rules.Inventory, doc []byte) error {
	var meta struct {
		Kind  string            `json:"kind"`
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(doc, &meta); err != nil {
		return err
	}
	decode := func(v any) error { return json.NewDecoder(bytes.NewReader(doc)).Decode(v) }
	switch meta.Kind {
	case "":
		// Not a Kubernetes object (for example a values file): ignore it.
	case "List":
		for _, item := range meta.Items {
			if err := addJSON(inv, item); err != nil {
				return err
			}
		}
	case "Deployment":
		var o appsv1.Deployment
		if err := decode(&o); err != nil {
			return err
		}
		inv.Workloads = append(inv.Workloads, rules.FromDeployment(o))
	case "StatefulSet":
		var o appsv1.StatefulSet
		if err := decode(&o); err != nil {
			return err
		}
		inv.Workloads = append(inv.Workloads, rules.FromStatefulSet(o))
	case "Pod":
		var o corev1.Pod
		if err := decode(&o); err != nil {
			return err
		}
		// A Pod owned by a controller is already covered by that controller.
		if len(o.OwnerReferences) == 0 {
			inv.Workloads = append(inv.Workloads, rules.FromPod(o))
		}
	case "PodDisruptionBudget":
		var o policyv1.PodDisruptionBudget
		if err := decode(&o); err != nil {
			return err
		}
		inv.PDBs = append(inv.PDBs, o)
	case "HorizontalPodAutoscaler":
		var o autoscalingv2.HorizontalPodAutoscaler
		if err := decode(&o); err != nil {
			return err
		}
		inv.HPAs = append(inv.HPAs, o)
	case "DaemonSet", "Job", "CronJob", "ReplicaSet":
		inv.Skipped++
	}
	return nil
}
