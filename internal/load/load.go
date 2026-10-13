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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/robertasolimandonofreo/spot-readiness/internal/karpenter"
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
		APIVersion string            `json:"apiVersion"`
		Kind       string            `json:"kind"`
		Items      []json.RawMessage `json:"items"`
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
		if o.Name == "" {
			inv.Skipped++ // an unrendered template, not a real object
			break
		}
		inv.Workloads = append(inv.Workloads, rules.FromDeployment(o))
	case "StatefulSet":
		var o appsv1.StatefulSet
		if err := decode(&o); err != nil {
			return err
		}
		if o.Name == "" {
			inv.Skipped++ // an unrendered template, not a real object
			break
		}
		inv.Workloads = append(inv.Workloads, rules.FromStatefulSet(o))
	case "Pod":
		var o corev1.Pod
		if err := decode(&o); err != nil {
			return err
		}
		// A Pod owned by a controller is already covered by that controller.
		if len(o.OwnerReferences) == 0 && o.Name != "" {
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
	case "Job":
		var o batchv1.Job
		if err := decode(&o); err != nil {
			return err
		}
		// A Job created by a CronJob is covered by that CronJob.
		if len(o.OwnerReferences) > 0 {
			break
		}
		if o.Name == "" {
			inv.Skipped++
			break
		}
		inv.Workloads = append(inv.Workloads, rules.FromJob(o))
	case "CronJob":
		var o batchv1.CronJob
		if err := decode(&o); err != nil {
			return err
		}
		if o.Name == "" || (o.Spec.Suspend != nil && *o.Spec.Suspend) {
			inv.Skipped++ // an unrendered template or a suspended schedule
			break
		}
		inv.Workloads = append(inv.Workloads, rules.FromCronJob(o))
	case "Rollout":
		if !strings.HasPrefix(meta.APIVersion, "argoproj.io/") {
			break
		}
		var o struct {
			Metadata metav1.ObjectMeta `json:"metadata"`
			Spec     rules.RolloutSpec `json:"spec"`
		}
		if err := decode(&o); err != nil {
			return err
		}
		w, ok := rules.FromRollout(o.Metadata.Namespace, o.Metadata.Name, o.Spec)
		if !ok || o.Metadata.Name == "" {
			inv.Skipped++ // takes its template from a Deployment, which is analyzed instead
			break
		}
		inv.Workloads = append(inv.Workloads, w)
	case "ScaledJob":
		if !strings.HasPrefix(meta.APIVersion, "keda.sh/") {
			break
		}
		var o struct {
			Metadata metav1.ObjectMeta   `json:"metadata"`
			Spec     rules.ScaledJobSpec `json:"spec"`
		}
		if err := decode(&o); err != nil {
			return err
		}
		if o.Metadata.Name == "" {
			inv.Skipped++ // an unrendered template, not a real object
			break
		}
		inv.Workloads = append(inv.Workloads, rules.FromScaledJob(o.Metadata.Namespace, o.Metadata.Name, o.Spec))
	case "ScaledObject":
		if !strings.HasPrefix(meta.APIVersion, "keda.sh/") {
			break
		}
		var o struct {
			Metadata metav1.ObjectMeta      `json:"metadata"`
			Spec     rules.ScaledObjectSpec `json:"spec"`
		}
		if err := decode(&o); err != nil {
			return err
		}
		inv.Scalers = append(inv.Scalers, rules.ScalerFromScaledObject(o.Metadata.Namespace, o.Spec))
	case "NodePool":
		if !strings.HasPrefix(meta.APIVersion, "karpenter.sh/") {
			break
		}
		var o struct {
			Metadata metav1.ObjectMeta      `json:"metadata"`
			Spec     karpenter.NodePoolSpec `json:"spec"`
		}
		if err := decode(&o); err != nil {
			return err
		}
		if o.Metadata.Name != "" {
			inv.Karpenter.NodePools = append(inv.Karpenter.NodePools, karpenter.NodePool{Name: o.Metadata.Name, Spec: o.Spec})
		}
	case "NodeClaim":
		if !strings.HasPrefix(meta.APIVersion, "karpenter.sh/") {
			break
		}
		var o struct {
			Metadata metav1.ObjectMeta `json:"metadata"`
		}
		if err := decode(&o); err != nil {
			return err
		}
		inv.Karpenter.NodeClaims = append(inv.Karpenter.NodeClaims,
			karpenter.NodeClaim{Name: o.Metadata.Name, NodePool: o.Metadata.Labels["karpenter.sh/nodepool"]})
	case "EC2NodeClass":
		if !strings.HasPrefix(meta.APIVersion, "karpenter.k8s.aws/") {
			break
		}
		var o struct {
			Metadata metav1.ObjectMeta `json:"metadata"`
			Status   struct {
				Subnets []struct {
					Zone string `json:"zone"`
				} `json:"subnets"`
			} `json:"status"`
		}
		if err := decode(&o); err != nil {
			return err
		}
		c := karpenter.NodeClass{Name: o.Metadata.Name}
		for _, sn := range o.Status.Subnets {
			c.Zones = append(c.Zones, sn.Zone)
		}
		inv.Karpenter.NodeClasses = append(inv.Karpenter.NodeClasses, c)
	case "DaemonSet":
		var o appsv1.DaemonSet
		if err := decode(&o); err != nil {
			return err
		}
		inv.DaemonSets = append(inv.DaemonSets, rules.FromDaemonSet(o))
		inv.Skipped++
	case "ReplicaSet":
		inv.Skipped++
	}
	return nil
}
