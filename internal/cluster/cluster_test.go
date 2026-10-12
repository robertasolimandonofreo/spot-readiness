package cluster

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

	all, err := Load(context.Background(), client, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Workloads) != 4 || all.Skipped != 1 {
		t.Fatalf("all namespaces: got %d workloads and %d skipped, want 4 and 1", len(all.Workloads), all.Skipped)
	}

	shop, err := Load(context.Background(), client, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(shop.Workloads) != 3 {
		t.Fatalf("namespace shop: got %d workloads, want 3 (web, db, bare pod)", len(shop.Workloads))
	}
}
