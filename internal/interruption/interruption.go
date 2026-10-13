// Package interruption assesses the cluster, not a workload: when AWS reclaims a Spot node, is
// something in the cluster ready to cordon, drain and replace it before the node disappears?
package interruption

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/robertasolimandonofreo/spot-readiness/internal/karpenter"
	"github.com/robertasolimandonofreo/spot-readiness/internal/rules"
)

// Tri-state answers: the tool cannot always tell "no" from "could not see".
const (
	Yes     = "yes"
	No      = "no"
	Unknown = "unknown"
	NA      = "n/a"
)

// Assessment is the cluster's readiness for Spot interruptions.
type Assessment struct {
	KarpenterDetected bool `json:"karpenterDetected"`
	// InterruptionQueue is whether Karpenter is configured with an SQS interruption queue: "yes",
	// "no", "unknown" (NodePools are visible but the controller is not) or "n/a".
	InterruptionQueue      string `json:"interruptionQueue"`
	SpotDetected           bool   `json:"spotDetected"`
	SpotNodes              int    `json:"spotNodes"`
	NodeTerminationHandler bool   `json:"nodeTerminationHandler"`
	// PDBIssues counts workloads with no PodDisruptionBudget (SR005) or one that allows no
	// disruption (SR014): the first lets a drain remove every replica, the second stalls it.
	PDBIssues int `json:"pdbIssues"`
	// SlowTermination counts workloads whose grace period exceeds the 2-minute Spot notice (SR003).
	SlowTermination int `json:"slowTerminationWorkloads"`
	// Score is nil when no Spot capacity was detected, because there is nothing to interrupt.
	Score *int     `json:"score"`
	Notes []string `json:"notes"`
}

func hasEnv(c corev1.Container, name string) bool {
	for _, e := range c.Env {
		if e.Name == name && (e.Value != "" || e.ValueFrom != nil) {
			return true
		}
	}
	return false
}

func mentions(c corev1.Container, text string) bool {
	for _, a := range append(append([]string{}, c.Command...), c.Args...) {
		if strings.Contains(a, text) {
			return true
		}
	}
	return false
}

// findKarpenter looks for the Karpenter controller Deployment and says whether it has an
// interruption queue (the INTERRUPTION_QUEUE variable or --interruption-queue flag).
func findKarpenter(inv *rules.Inventory) (found, queue bool) {
	for _, w := range inv.Workloads {
		if w.Kind != "Deployment" {
			continue
		}
		for _, c := range w.Template.Spec.Containers {
			if w.Name == "karpenter" || strings.Contains(c.Image, "karpenter/controller") {
				found = true
				if hasEnv(c, "INTERRUPTION_QUEUE") || mentions(c, "--interruption-queue") {
					queue = true
				}
			}
		}
	}
	return found, queue
}

func findNTH(inv *rules.Inventory) bool {
	for _, group := range [][]rules.Workload{inv.Workloads, inv.DaemonSets} {
		for _, w := range group {
			if strings.Contains(w.Name, "node-termination-handler") {
				return true
			}
			for _, c := range w.Template.Spec.Containers {
				if strings.Contains(c.Image, "aws-node-termination-handler") {
					return true
				}
			}
		}
	}
	return false
}

// Assess reads the cluster-level pieces from the inventory and the per-workload findings from the
// analysis results. The score starts at 100 and loses 40 when nothing handles the interruption
// notice, 5 when both Karpenter's queue and the Node Termination Handler do, 5 per workload with
// a PodDisruptionBudget problem (up to 25) and 4 per workload with a termination grace period above
// the notice (up to 16).
func Assess(inv *rules.Inventory, results []rules.Result) Assessment {
	a := Assessment{InterruptionQueue: NA, NodeTerminationHandler: findNTH(inv), Notes: []string{}}
	controller, queue := findKarpenter(inv)
	spot := inv.SpotNodes()
	a.KarpenterDetected = controller || len(inv.Karpenter.NodePools) > 0 || spot["karpenter"] > 0
	switch {
	case controller && queue:
		a.InterruptionQueue = Yes
	case controller:
		a.InterruptionQueue = No
	case a.KarpenterDetected:
		a.InterruptionQueue = Unknown
	}

	for _, n := range spot {
		a.SpotNodes += n
	}
	karpenterSpotPool := false
	for _, p := range inv.Karpenter.NodePools {
		if karpenter.AllowsSpot(p) {
			karpenterSpotPool = true
		}
	}
	pinned := false
	for _, r := range results {
		if r.OnSpot {
			pinned = true
		}
		for _, f := range r.Findings {
			switch f.Rule {
			case "SR005", "SR014":
				a.PDBIssues++
			case "SR003":
				a.SlowTermination++
			}
		}
	}
	a.SpotDetected = a.SpotNodes > 0 || karpenterSpotPool || pinned
	if !a.SpotDetected {
		a.Notes = append(a.Notes, "no Spot capacity found: nothing to assess")
		return a
	}

	score := 100
	// Karpenter nodes need the queue or the handler; nodes from an unmanaged Auto Scaling group
	// need the handler; EKS managed node groups drain Spot nodes on their own (capacity rebalancing).
	needsKarpenterHandling := spot["karpenter"] > 0 || karpenterSpotPool
	handledByKarpenter := a.InterruptionQueue == Yes || a.InterruptionQueue == Unknown
	switch {
	case needsKarpenterHandling && !handledByKarpenter && !a.NodeTerminationHandler:
		score -= 40
		a.Notes = append(a.Notes, "Karpenter has no interruption queue and no Node Termination Handler runs: a Spot interruption notice is not acted on, and the node is simply gone after 2 minutes")
	case spot[""] > 0 && !a.NodeTerminationHandler:
		score -= 40
		a.Notes = append(a.Notes, fmt.Sprintf("%d Spot node(s) were not launched by Karpenter or an EKS managed node group, and no Node Termination Handler runs: nothing drains them on an interruption notice", spot[""]))
	}
	if a.InterruptionQueue == Unknown {
		a.Notes = append(a.Notes, "NodePools were found but not the Karpenter controller, so its interruption queue could not be checked")
	}
	if a.InterruptionQueue == Yes && a.NodeTerminationHandler {
		score -= 5
		a.Notes = append(a.Notes, "both the Karpenter interruption queue and a Node Termination Handler react to the same events; Karpenter's documentation recommends running only one")
	}
	score -= min(5*a.PDBIssues, 25) + min(4*a.SlowTermination, 16)
	if a.PDBIssues > 0 {
		a.Notes = append(a.Notes, fmt.Sprintf("%d workload(s) have no PodDisruptionBudget or one that blocks every eviction (SR005, SR014)", a.PDBIssues))
	}
	if a.SlowTermination > 0 {
		a.Notes = append(a.Notes, fmt.Sprintf("%d workload(s) need more than 120s to terminate, so they are cut off by the Spot notice (SR003)", a.SlowTermination))
	}
	score = max(score, 0)
	a.Score = &score
	return a
}

// Prerequisites lists what the cluster still needs, before any workload moves, to act on a Spot
// interruption notice. Unlike Assess it does not need Spot nodes to exist yet. It is only reliable
// when the whole cluster was read, since Karpenter and the handler live in their own namespaces.
func Prerequisites(inv *rules.Inventory) []string {
	controller, queue := findKarpenter(inv)
	nth := findNTH(inv)
	switch {
	case controller && queue && nth:
		return []string{"Run only one of Karpenter's interruption queue and the Node Termination Handler: both react to the same events."}
	case controller && !queue && !nth:
		return []string{"Configure Karpenter's interruption queue (settings.interruptionQueue) so it can drain and replace a Spot node when AWS sends the 2-minute notice."}
	case !controller && !nth && len(inv.Karpenter.NodePools) == 0:
		return []string{"Set up handling of Spot interruption notices: Karpenter with an interruption queue, EKS managed node groups (they drain Spot nodes themselves), or the AWS Node Termination Handler."}
	}
	return nil
}
