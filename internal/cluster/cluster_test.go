package cluster

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

func i32(v int32) *int32 { return &v }

func TestLoad(t *testing.T) {
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Image: "x"}}}
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"},
			Spec: appsv1.DeploymentSpec{Replicas: i32(3), Template: corev1.PodTemplateSpec{Spec: podSpec}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "elsewhere"},
			Spec: appsv1.DeploymentSpec{Replicas: i32(2), Template: corev1.PodTemplateSpec{Spec: podSpec}}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"},
			Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: podSpec}}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "shop"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "shop"}, Spec: podSpec},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "shop",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs"}}}, Spec: podSpec},
	)

	all, err := Load(context.Background(), Clients{Kube: client}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Workloads) != 4 || all.Skipped != 1 {
		t.Fatalf("all namespaces: got %d workloads and %d skipped, want 4 and 1", len(all.Workloads), all.Skipped)
	}

	if len(all.DaemonSets) != 1 || all.DaemonSets[0].Name != "agent" {
		t.Fatalf("daemonsets: %+v", all.DaemonSets)
	}

	shop, err := Load(context.Background(), Clients{Kube: client}, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(shop.Workloads) != 3 {
		t.Fatalf("namespace shop: got %d workloads, want 3 (web, db, bare pod)", len(shop.Workloads))
	}
}

func TestLoadJobsAndCustomResources(t *testing.T) {
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Image: "x"}}}
	yes := true
	now := metav1.Now()
	client := fake.NewSimpleClientset(
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "jobs"},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: podSpec}}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "jobs"},
			Spec:   batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: podSpec}},
			Status: batchv1.JobStatus{CompletionTime: &now}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "from-cron", Namespace: "jobs",
			OwnerReferences: []metav1.OwnerReference{{Kind: "CronJob", Name: "nightly"}}}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "jobs"},
			Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{Spec: podSpec}}}}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "paused", Namespace: "jobs"},
			Spec: batchv1.CronJobSpec{Suspend: &yes}},
	)
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList", scaledObjectGVR: "ScaledObjectList", scaledJobGVR: "ScaledJobList"},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
			"metadata": map[string]any{"name": "web", "namespace": "shop"},
			"spec": map[string]any{"replicas": int64(3), "template": map[string]any{
				"spec": map[string]any{"containers": []any{map[string]any{"name": "web", "image": "x"}}}}},
		}},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledJob",
			"metadata": map[string]any{"name": "consumer", "namespace": "jobs"},
			"spec": map[string]any{"jobTargetRef": map[string]any{"backoffLimit": int64(0), "template": map[string]any{
				"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "image": "x"}}}}}},
		}},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject",
			"metadata": map[string]any{"name": "api", "namespace": "shop"},
			"spec":     map[string]any{"scaleTargetRef": map[string]any{"name": "api"}, "minReplicaCount": int64(2)},
		}},
	)

	inv, err := Load(context.Background(), Clients{Kube: client, Dynamic: dyn}, "")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, w := range inv.Workloads {
		kinds[w.Kind]++
	}
	// running Job, nightly CronJob, the Rollout and the ScaledJob; "done", the CronJob-owned Job and the
	// suspended CronJob are not workloads.
	if kinds["Job"] != 1 || kinds["CronJob"] != 1 || kinds["Rollout"] != 1 || kinds["ScaledJob"] != 1 || len(inv.Workloads) != 4 {
		t.Fatalf("workloads by kind: %v", kinds)
	}
	if inv.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2 (finished job, suspended cronjob)", inv.Skipped)
	}
	if len(inv.Scalers) != 1 || inv.Scalers[0].MinReplicas != 2 {
		t.Fatalf("scalers: %+v", inv.Scalers)
	}

	// Without a dynamic client the custom resources are simply not read.
	inv, err = Load(context.Background(), Clients{Kube: client}, "")
	if err != nil || len(inv.Scalers) != 0 {
		t.Fatalf("no dynamic client: %v, %d scalers", err, len(inv.Scalers))
	}
}

func TestLoadPlacesPodsOnNodes(t *testing.T) {
	yes := true
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Image: "x"}}}
	pod := func(name string, owner metav1.OwnerReference, node string, phase corev1.PodPhase) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"}, Spec: podSpec, Status: corev1.PodStatus{Phase: phase}}
		p.Spec.NodeName = node
		if owner.Kind != "" {
			owner.Controller = &yes
			p.OwnerReferences = []metav1.OwnerReference{owner}
		}
		return p
	}
	rs := metav1.OwnerReference{Kind: "ReplicaSet", Name: "web-abc"}
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"},
			Spec: appsv1.DeploymentSpec{Replicas: i32(3), Template: corev1.PodTemplateSpec{Spec: podSpec}}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "shop",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web"}}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{
			"topology.kubernetes.io/zone": "a", "karpenter.sh/capacity-type": "spot"}}},
		pod("web-1", rs, "n1", corev1.PodRunning),
		pod("web-2", rs, "n1", corev1.PodRunning),
		pod("web-3", rs, "n1", corev1.PodSucceeded),
		pod("web-4", rs, "", corev1.PodPending),
	)
	inv, err := Load(context.Background(), Clients{Kube: client}, "")
	if err != nil {
		t.Fatal(err)
	}
	res := rules.Analyze(inv, false)
	if len(res) != 1 || res[0].Runtime == nil || res[0].Runtime.Pods != 2 || res[0].Runtime.Resilience != "low" {
		t.Fatalf("runtime: %+v", res)
	}
}

func TestLoadKarpenter(t *testing.T) {
	obj := func(group, version, kind, name string, extra map[string]any) *unstructured.Unstructured {
		o := map[string]any{"apiVersion": group + "/" + version, "kind": kind, "metadata": map[string]any{"name": name}}
		for k, v := range extra {
			o[k] = v
		}
		return &unstructured.Unstructured{Object: o}
	}
	nodePool := obj("karpenter.sh", "v1", "NodePool", "spot", map[string]any{"spec": map[string]any{
		"template":   map[string]any{"spec": map[string]any{"nodeClassRef": map[string]any{"name": "default"}}},
		"disruption": map[string]any{"budgets": []any{map[string]any{"nodes": "10%"}}},
	}})
	claim := obj("karpenter.sh", "v1", "NodeClaim", "spot-1", nil)
	claim.SetLabels(map[string]string{"karpenter.sh/nodepool": "spot"})
	class := obj("karpenter.k8s.aws", "v1", "EC2NodeClass", "default", map[string]any{"status": map[string]any{
		"subnets": []any{map[string]any{"id": "s-1", "zone": "us-east-1a"}}}})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}:           "NodePoolList",
			{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}:          "NodeClaimList",
			{Group: "karpenter.k8s.aws", Version: "v1", Resource: "ec2nodeclasses"}: "EC2NodeClassList",
		}, nodePool, claim, class)

	inv := &rules.Inventory{}
	LoadKarpenter(context.Background(), Clients{Kube: fake.NewSimpleClientset(), Dynamic: dyn}, inv)
	k := inv.Karpenter
	if len(k.NodePools) != 1 || k.NodePools[0].Spec.Template.Spec.NodeClassRef.Name != "default" ||
		len(k.NodeClaims) != 1 || k.NodeClaims[0].NodePool != "spot" || len(k.NodeClasses) != 1 || k.NodeClasses[0].Zones[0] != "us-east-1a" {
		t.Fatalf("karpenter inventory: %+v", k)
	}
	// Without a dynamic client nothing is read.
	empty := &rules.Inventory{}
	LoadKarpenter(context.Background(), Clients{Kube: fake.NewSimpleClientset()}, empty)
	if len(empty.Karpenter.NodePools) != 0 {
		t.Fatal("no dynamic client: expected nothing")
	}
}
