package rules

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestConfidence(t *testing.T) {
	pvc := corev1.Volume{Name: "d", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "d"}}}
	cases := []struct {
		name   string
		mutate func(w *Workload)
		want   string
		reason string
	}{
		{"a stateless deployment with nothing hidden", func(*Workload) {}, ConfidenceHigh, ""},
		{"a StatefulSet without volumes", func(w *Workload) { w.Kind = "StatefulSet" }, ConfidenceMedium, "StatefulSet"},
		{"a Job", func(w *Workload) { w.Batch = true }, ConfidenceMedium, "checkpoints"},
		{"a bare Pod", func(w *Workload) { w.Kind = "Pod" }, ConfidenceMedium, "bare Pod"},
		{"scheduling constraints", func(w *Workload) {
			w.Template.Spec.NodeSelector = map[string]string{"topology.kubernetes.io/zone": "a", "pool": "x"}
		}, ConfidenceMedium, "scheduling constraints"},
		{"persistent volume", func(w *Workload) { w.Template.Spec.Volumes = []corev1.Volume{pvc} }, ConfidenceLow, "persistent state"},
		{"StatefulSet with claim templates and constraints", func(w *Workload) {
			w.Kind, w.ClaimTemplates = "StatefulSet", true
			w.Template.Spec.NodeSelector = map[string]string{"topology.kubernetes.io/zone": "a", "pool": "x"}
		}, ConfidenceLow, "persistent state"},
		{"hostPath", func(w *Workload) {
			w.Template.Spec.Volumes = []corev1.Volume{{Name: "h", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/x"}}}}
		}, ConfidenceLow, "hostPath"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := healthy()
			c.mutate(&w)
			r := Evaluate(inventoryWithPDB(), w)
			if r.Confidence != c.want {
				t.Fatalf("confidence = %s, want %s (%v)", r.Confidence, c.want, r.ConfidenceReasons)
			}
			if !strings.Contains(strings.Join(r.ConfidenceReasons, "\n"), c.reason) {
				t.Fatalf("reasons = %v, want one containing %q", r.ConfidenceReasons, c.reason)
			}
		})
	}
}

// Confidence says how far to trust the verdict. It never changes the score.
func TestConfidenceDoesNotChangeTheScore(t *testing.T) {
	w := healthy()
	w.Kind, w.ClaimTemplates = "StatefulSet", true
	r := Evaluate(inventoryWithPDB(), w)
	want := 100 - Penalty[Caveat] // SR007 only
	if r.Confidence != ConfidenceLow || r.Score != want {
		t.Fatalf("confidence %s, score %d (want %d)", r.Confidence, r.Score, want)
	}
}
