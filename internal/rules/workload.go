// Package rules decides whether a Kubernetes workload can run on Spot capacity.
package rules

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Workload is the common view of a Deployment, StatefulSet, Argo Rollout, Job, CronJob or bare Pod.
type Workload struct {
	Kind      string
	Namespace string
	Name      string
	// Replicas is spec.replicas, defaulting to 1 when unset.
	Replicas int32
	Template corev1.PodTemplateSpec
	// ClaimTemplates is true for StatefulSets that declare volumeClaimTemplates.
	ClaimTemplates bool
	// Batch is true for Jobs and CronJobs, where Replicas is the job's parallelism.
	Batch bool
	// BackoffLimit and ActiveDeadlineSeconds are the Job settings that decide what happens when a
	// pod is lost. IgnoresDisruption is true when a podFailurePolicy ignores DisruptionTarget.
	BackoffLimit          *int32
	ActiveDeadlineSeconds *int64
	IgnoresDisruption     bool
}

// Scaler is an autoscaler that is not an HPA object but still sets a minimum replica count, such
// as a KEDA ScaledObject (KEDA creates the HPA at runtime).
type Scaler struct {
	Namespace   string
	Kind        string
	Name        string
	MinReplicas int32
}

// Inventory is everything loaded from the manifests or the cluster.
type Inventory struct {
	Workloads []Workload
	PDBs      []policyv1.PodDisruptionBudget
	HPAs      []autoscalingv2.HorizontalPodAutoscaler
	Scalers   []Scaler
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

func jobWorkload(kind, ns, name string, spec batchv1.JobSpec) Workload {
	parallelism := int32(1)
	if spec.Parallelism != nil {
		parallelism = *spec.Parallelism
	}
	ignores := false
	if spec.PodFailurePolicy != nil {
		for _, r := range spec.PodFailurePolicy.Rules {
			if r.Action != batchv1.PodFailurePolicyActionIgnore {
				continue
			}
			for _, c := range r.OnPodConditions {
				if c.Type == corev1.DisruptionTarget {
					ignores = true
				}
			}
		}
	}
	return Workload{Kind: kind, Namespace: namespaceOrDefault(ns), Name: name, Replicas: parallelism,
		Template: spec.Template, Batch: true, BackoffLimit: spec.BackoffLimit,
		ActiveDeadlineSeconds: spec.ActiveDeadlineSeconds, IgnoresDisruption: ignores}
}

func FromJob(j batchv1.Job) Workload {
	return jobWorkload("Job", j.Namespace, j.Name, j.Spec)
}

func FromCronJob(c batchv1.CronJob) Workload {
	return jobWorkload("CronJob", c.Namespace, c.Name, c.Spec.JobTemplate.Spec)
}

func FromPod(p corev1.Pod) Workload {
	return Workload{Kind: "Pod", Namespace: namespaceOrDefault(p.Namespace), Name: p.Name, Replicas: 1,
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: p.Labels}, Spec: p.Spec}}
}

// MinReplicas is the lowest replica count the workload can have: the HPA (or KEDA) minimum when
// one targets it, otherwise spec.replicas.
func (inv *Inventory) MinReplicas(w Workload) int32 {
	for _, sc := range inv.Scalers {
		if namespaceOrDefault(sc.Namespace) == w.Namespace && sc.Kind == w.Kind && sc.Name == w.Name {
			return sc.MinReplicas
		}
	}
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
