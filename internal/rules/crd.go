package rules

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
)

// RolloutSpec is the part of an Argo Rollouts "Rollout" that matters here.
type RolloutSpec struct {
	Replicas *int32                 `json:"replicas"`
	Template corev1.PodTemplateSpec `json:"template"`
	// WorkloadRef is set when the Rollout takes its pod template from a Deployment.
	WorkloadRef json.RawMessage `json:"workloadRef"`
}

// FromRollout returns false for a Rollout that references a Deployment: that Deployment is the
// workload to analyze.
func FromRollout(namespace, name string, spec RolloutSpec) (Workload, bool) {
	if len(spec.WorkloadRef) > 0 && string(spec.WorkloadRef) != "null" {
		return Workload{}, false
	}
	return Workload{Kind: "Rollout", Namespace: namespaceOrDefault(namespace), Name: name,
		Replicas: replicasOrOne(spec.Replicas), Template: spec.Template}, true
}

// ScaledObjectSpec is the part of a KEDA "ScaledObject" that matters here.
type ScaledObjectSpec struct {
	ScaleTargetRef struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"scaleTargetRef"`
	// MinReplicaCount defaults to 0 in KEDA, which allows scaling to zero.
	MinReplicaCount *int32 `json:"minReplicaCount"`
}

// ScalerFromScaledObject turns a ScaledObject into a Scaler. KEDA targets Deployments by default.
func ScalerFromScaledObject(namespace string, spec ScaledObjectSpec) Scaler {
	kind := spec.ScaleTargetRef.Kind
	if kind == "" {
		kind = "Deployment"
	}
	var min int32
	if spec.MinReplicaCount != nil {
		min = *spec.MinReplicaCount
	}
	return Scaler{Namespace: namespace, Kind: kind, Name: spec.ScaleTargetRef.Name, MinReplicas: min}
}
