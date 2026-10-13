package rules

// Confidence says how far the verdict can be trusted as a prediction. The score counts what the
// manifest shows; confidence counts what it cannot show, such as whether an application replicates
// and recovers its own state. A workload with no such unknowns has high confidence.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// confidenceOf weighs the unknowns of a workload: one unknown gives medium confidence, two or more low.
func confidenceOf(w Workload, flex Flexibility) (string, []string) {
	points := 0
	reasons := []string{}
	add := func(weight int, reason string) {
		points += weight
		reasons = append(reasons, reason)
	}
	persistent := w.ClaimTemplates
	hostPath := false
	for _, v := range w.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			persistent = true
		}
		if v.HostPath != nil {
			hostPath = true
		}
	}
	if persistent {
		add(2, "persistent state: the manifest does not show how the application replicates or recovers it")
	}
	if hostPath {
		add(2, "node-local data in a hostPath volume")
	}
	if w.Kind == "StatefulSet" {
		add(1, "StatefulSet: pod identity and ordering matter, and the manifest does not show how much")
	}
	if w.Batch {
		add(1, "job: whether it checkpoints its progress cannot be verified")
	}
	if w.Kind == "Pod" {
		add(1, "bare Pod: no controller shows how it is meant to run")
	}
	if flex.Constraints != "low" {
		add(1, "scheduling constraints narrow the capacity pools, and the manifest does not show the nodes behind them")
	}
	switch {
	case points == 0:
		return ConfidenceHigh, reasons
	case points == 1:
		return ConfidenceMedium, reasons
	}
	return ConfidenceLow, reasons
}
