// Package rules decides whether a Kubernetes workload can run on Spot capacity.
package rules

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Workload is the common view of a Deployment, StatefulSet or bare Pod.
type Workload struct {
	Kind      string
	Namespace string
	Name      string
	// Replicas is spec.replicas, defaulting to 1 when unset.
	Replicas int32
	Template corev1.PodTemplateSpec
	// ClaimTemplates is true for StatefulSets that declare volumeClaimTemplates.
	ClaimTemplates bool
}

// Inventory is everything loaded from the manifests or the cluster.
type Inventory struct {
	Workloads []Workload
	PDBs      []policyv1.PodDisruptionBudget
	HPAs      []autoscalingv2.HorizontalPodAutoscaler
	// Skipped counts objects that were recognised but are out of scope (for example DaemonSets).
	Skipped int
}

func namespaceOrDefault(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

func replicasOrOne(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

func FromDeployment(d appsv1.Deployment) Workload {
	return Workload{Kind: "Deployment", Namespace: namespaceOrDefault(d.Namespace), Name: d.Name,
		Replicas: replicasOrOne(d.Spec.Replicas), Template: d.Spec.Template}
}

func FromStatefulSet(s appsv1.StatefulSet) Workload {
	return Workload{Kind: "StatefulSet", Namespace: namespaceOrDefault(s.Namespace), Name: s.Name,
		Replicas: replicasOrOne(s.Spec.Replicas), Template: s.Spec.Template,
		ClaimTemplates: len(s.Spec.VolumeClaimTemplates) > 0}
}

func FromPod(p corev1.Pod) Workload {
	return Workload{Kind: "Pod", Namespace: namespaceOrDefault(p.Namespace), Name: p.Name, Replicas: 1,
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: p.Labels}, Spec: p.Spec}}
}

// MinReplicas is the lowest replica count the workload can have: the HPA minimum when one
// targets it, otherwise spec.replicas.
func (inv *Inventory) MinReplicas(w Workload) int32 {
	for _, h := range inv.HPAs {
		if namespaceOrDefault(h.Namespace) != w.Namespace {
			continue
		}
		ref := h.Spec.ScaleTargetRef
		if ref.Kind == w.Kind && ref.Name == w.Name {
			if h.Spec.MinReplicas != nil {
				return *h.Spec.MinReplicas
			}
			return 1
		}
	}
	return w.Replicas
}

// HasPDB reports whether a PodDisruptionBudget selects the workload's pods.
func (inv *Inventory) HasPDB(w Workload) bool {
	for _, p := range inv.PDBs {
		if namespaceOrDefault(p.Namespace) != w.Namespace || p.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil || sel.Empty() {
			continue
		}
		if sel.Matches(labels.Set(w.Template.Labels)) {
			return true
		}
	}
	return false
}
