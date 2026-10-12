// Package cluster reads workloads from a live Kubernetes cluster, using list calls only.
package cluster

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

// Options selects the cluster and, optionally, one namespace. Empty values use the kubeconfig
// defaults (KUBECONFIG, then ~/.kube/config, and its current context).
type Options struct {
	Kubeconfig string
	Context    string
	Namespace  string // empty means all namespaces
}

// Clients holds the typed client and the dynamic client used for custom resources (Argo
// Rollouts and KEDA). Dynamic may be nil, which skips the custom resources.
type Clients struct {
	Kube    kubernetes.Interface
	Dynamic dynamic.Interface
}

// Connect builds the clients for the selected cluster.
func Connect(opts Options) (Clients, string, error) {
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loading.ExplicitPath = opts.Kubeconfig
	}
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading,
		&clientcmd.ConfigOverrides{CurrentContext: opts.Context})
	rest, err := cfg.ClientConfig()
	if err != nil {
		return Clients{}, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(rest)
	if err != nil {
		return Clients{}, "", err
	}
	dyn, err := dynamic.NewForConfig(rest)
	if err != nil {
		return Clients{}, "", err
	}
	raw, _ := cfg.RawConfig()
	name := opts.Context
	if name == "" {
		name = raw.CurrentContext
	}
	return Clients{Kube: client, Dynamic: dyn}, name, nil
}

var (
	rolloutGVR      = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	scaledObjectGVR = schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
)

func jobFinished(j batchv1.Job) bool {
	if j.Status.CompletionTime != nil {
		return true
	}
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == "True" {
			return true
		}
	}
	return false
}

// Load lists Deployments, StatefulSets, Jobs, CronJobs, bare Pods, PodDisruptionBudgets and HPAs,
// plus Argo Rollouts and KEDA ScaledObjects when those CRDs exist. DaemonSets are counted as
// skipped, like in file mode.
func Load(ctx context.Context, c Clients, namespace string) (*rules.Inventory, error) {
	client := c.Kube
	inv := &rules.Inventory{}
	ns := namespace // metav1.NamespaceAll is "", so an empty namespace lists everything
	opts := metav1.ListOptions{}

	deployments, err := client.AppsV1().Deployments(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	for _, d := range deployments.Items {
		inv.Workloads = append(inv.Workloads, rules.FromDeployment(d))
	}

	statefulSets, err := client.AppsV1().StatefulSets(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list statefulsets: %w", err)
	}
	for _, s := range statefulSets.Items {
		inv.Workloads = append(inv.Workloads, rules.FromStatefulSet(s))
	}

	pods, err := client.CoreV1().Pods(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	for _, p := range pods.Items {
		// Pods owned by a controller are covered by that controller.
		if len(p.OwnerReferences) == 0 {
			inv.Workloads = append(inv.Workloads, rules.FromPod(p))
		}
	}

	pdbs, err := client.PolicyV1().PodDisruptionBudgets(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list poddisruptionbudgets: %w", err)
	}
	inv.PDBs = pdbs.Items

	hpas, err := client.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("list horizontalpodautoscalers: %w", err)
	}
	inv.HPAs = hpas.Items

	// The lists below are optional: a missing permission or CRD must not fail the scan.
	if l, err := client.AppsV1().DaemonSets(ns).List(ctx, opts); err == nil {
		inv.Skipped += len(l.Items)
	}
	if l, err := client.BatchV1().Jobs(ns).List(ctx, opts); err == nil {
		for _, j := range l.Items {
			switch {
			case len(j.OwnerReferences) > 0: // created by a CronJob, which is analyzed instead
			case jobFinished(j):
				inv.Skipped++
			default:
				inv.Workloads = append(inv.Workloads, rules.FromJob(j))
			}
		}
	}
	if l, err := client.BatchV1().CronJobs(ns).List(ctx, opts); err == nil {
		for _, cj := range l.Items {
			if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
				inv.Skipped++
				continue
			}
			inv.Workloads = append(inv.Workloads, rules.FromCronJob(cj))
		}
	}

	if c.Dynamic != nil {
		if l, err := c.Dynamic.Resource(rolloutGVR).Namespace(ns).List(ctx, opts); err == nil {
			for _, u := range l.Items {
				var o struct {
					Spec rules.RolloutSpec `json:"spec"`
				}
				if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &o) != nil {
					continue
				}
				if w, ok := rules.FromRollout(u.GetNamespace(), u.GetName(), o.Spec); ok {
					inv.Workloads = append(inv.Workloads, w)
				} else {
					inv.Skipped++
				}
			}
		}
		if l, err := c.Dynamic.Resource(scaledObjectGVR).Namespace(ns).List(ctx, opts); err == nil {
			for _, u := range l.Items {
				var o struct {
					Spec rules.ScaledObjectSpec `json:"spec"`
				}
				if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &o) != nil {
					continue
				}
				inv.Scalers = append(inv.Scalers, rules.ScalerFromScaledObject(u.GetNamespace(), o.Spec))
			}
		}
	}
	return inv, nil
}
