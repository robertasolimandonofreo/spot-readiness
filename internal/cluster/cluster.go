// Package cluster reads workloads from a live Kubernetes cluster, using list calls only.
package cluster

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// Connect builds a clientset for the selected cluster.
func Connect(opts Options) (kubernetes.Interface, string, error) {
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		loading.ExplicitPath = opts.Kubeconfig
	}
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading,
		&clientcmd.ConfigOverrides{CurrentContext: opts.Context})
	rest, err := cfg.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(rest)
	if err != nil {
		return nil, "", err
	}
	raw, _ := cfg.RawConfig()
	name := opts.Context
	if name == "" {
		name = raw.CurrentContext
	}
	return client, name, nil
}

// Load lists Deployments, StatefulSets, bare Pods, PodDisruptionBudgets and HPAs. DaemonSets, Jobs
// and CronJobs are counted as skipped, like in file mode.
func Load(ctx context.Context, client kubernetes.Interface, namespace string) (*rules.Inventory, error) {
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

	for _, count := range []func() (int, error){
		func() (int, error) {
			l, err := client.AppsV1().DaemonSets(ns).List(ctx, opts)
			if err != nil {
				return 0, err
			}
			return len(l.Items), nil
		},
		func() (int, error) {
			l, err := client.BatchV1().Jobs(ns).List(ctx, opts)
			if err != nil {
				return 0, err
			}
			return len(l.Items), nil
		},
		func() (int, error) {
			l, err := client.BatchV1().CronJobs(ns).List(ctx, opts)
			if err != nil {
				return 0, err
			}
			return len(l.Items), nil
		},
	} {
		// Skipped counts are informational: a missing permission here must not fail the scan.
		if n, err := count(); err == nil {
			inv.Skipped += n
		}
	}
	return inv, nil
}
