package rules

import (
	"fmt"
	"sort"
	"strings"
)

// Node is what the scheduler placed pods on, reduced to the labels that matter for Spot.
type Node struct {
	Name         string
	Zone         string
	CapacityType string // "spot", "on-demand" or "" when the node does not say
	InstanceType string
	// Provisioner is "karpenter" or "eks-managed" for nodes launched by Karpenter or by an EKS
	// managed node group, and empty for any other node (for example a self-managed Auto Scaling group).
	Provisioner string
}

// NodeFromLabels reads the zone, capacity type and instance type from the usual node labels
// (Kubernetes, Karpenter and EKS managed node groups).
func NodeFromLabels(name string, l map[string]string) Node {
	n := Node{Name: name, Zone: l["topology.kubernetes.io/zone"], InstanceType: l["node.kubernetes.io/instance-type"]}
	if n.Zone == "" {
		n.Zone = l["failure-domain.beta.kubernetes.io/zone"]
	}
	if n.InstanceType == "" {
		n.InstanceType = l["beta.kubernetes.io/instance-type"]
	}
	switch {
	case l["karpenter.sh/capacity-type"] != "" || l["karpenter.sh/nodepool"] != "":
		n.Provisioner = "karpenter"
	case l["eks.amazonaws.com/capacityType"] != "":
		n.Provisioner = "eks-managed"
	}
	switch {
	case l["karpenter.sh/capacity-type"] == "spot", l["eks.amazonaws.com/capacityType"] == "SPOT":
		n.CapacityType = "spot"
	case l["karpenter.sh/capacity-type"] != "":
		n.CapacityType = l["karpenter.sh/capacity-type"]
	case l["eks.amazonaws.com/capacityType"] == "ON_DEMAND":
		n.CapacityType = "on-demand"
	}
	return n
}

// SetNodes records the cluster's nodes and turns on runtime analysis.
func (inv *Inventory) SetNodes(nodes []Node) {
	inv.nodes = map[string]Node{}
	for _, n := range nodes {
		inv.nodes[n.Name] = n
	}
}

// HasNodes reports whether the cluster's nodes were read.
func (inv *Inventory) HasNodes() bool { return inv.nodes != nil }

// SpotNodes counts the Spot nodes by what launched them (see Node.Provisioner).
func (inv *Inventory) SpotNodes() map[string]int {
	out := map[string]int{}
	for _, n := range inv.nodes {
		if n.CapacityType == "spot" {
			out[n.Provisioner]++
		}
	}
	return out
}

// AddPod records that a pod of the given workload runs on a node.
func (inv *Inventory) AddPod(namespace, kind, name, node string) {
	if inv.pods == nil {
		inv.pods = map[string][]string{}
	}
	k := podKey(namespace, kind, name)
	inv.pods[k] = append(inv.pods[k], node)
}

func podKey(namespace, kind, name string) string { return namespace + "/" + kind + "/" + name }

// RuntimeNode is one node that runs pods of a workload.
type RuntimeNode struct {
	Name         string `json:"name"`
	Zone         string `json:"zone"`
	CapacityType string `json:"capacityType"`
	InstanceType string `json:"instanceType"`
	Pods         int    `json:"pods"`
}

// Runtime is where a workload's pods actually run right now. It answers a different question from
// the manifest rules: what happens if one node, or one zone, disappears.
type Runtime struct {
	Pods     int           `json:"pods"`
	Nodes    []RuntimeNode `json:"nodes"`
	Zones    int           `json:"zones"`
	SpotPods int           `json:"spotPods"`
	// BlastRadius is the share (percent) of the pods on the busiest node: what one lost node takes.
	BlastRadius int      `json:"blastRadiusPercent"`
	Resilience  string   `json:"resilience"` // "low", "medium" or "high"
	Warnings    []string `json:"warnings"`
}

// runtimeOf describes the placement of the workload's running pods, or nil when the cluster was
// not read or no pod of the workload was found.
func (inv *Inventory) runtimeOf(w Workload) *Runtime {
	pods := inv.pods[podKey(w.Namespace, w.Kind, w.Name)]
	if inv.nodes == nil || len(pods) == 0 {
		return nil
	}
	perNode := map[string]int{}
	for _, n := range pods {
		perNode[n]++
	}
	rt := &Runtime{Pods: len(pods), Warnings: []string{}}
	zones := map[string]bool{}
	var worst string
	for name, count := range perNode {
		n, ok := inv.nodes[name]
		if !ok {
			n = Node{Name: name}
		}
		rt.Nodes = append(rt.Nodes, RuntimeNode{Name: name, Zone: n.Zone, CapacityType: n.CapacityType,
			InstanceType: n.InstanceType, Pods: count})
		if n.Zone != "" {
			zones[n.Zone] = true
		}
		if n.CapacityType == "spot" {
			rt.SpotPods += count
		}
	}
	sort.Slice(rt.Nodes, func(i, j int) bool {
		if rt.Nodes[i].Pods != rt.Nodes[j].Pods {
			return rt.Nodes[i].Pods > rt.Nodes[j].Pods
		}
		return rt.Nodes[i].Name < rt.Nodes[j].Name
	})
	worst = rt.Nodes[0].Name
	rt.Zones = len(zones)
	rt.BlastRadius = (rt.Nodes[0].Pods*100 + rt.Pods/2) / rt.Pods

	zoneShare := map[string]int{}
	spotTypes := map[string]bool{}
	for _, n := range rt.Nodes {
		zoneShare[n.Zone] += n.Pods
		if n.CapacityType == "spot" {
			spotTypes[n.InstanceType] = true
		}
	}
	maxZone := 0
	for z, c := range zoneShare {
		if z != "" && c > maxZone {
			maxZone = c
		}
	}

	switch {
	case rt.Pods < 2:
		rt.Warnings = append(rt.Warnings, "Only 1 pod is running: losing its node means downtime.")
	case len(rt.Nodes) == 1:
		rt.Warnings = append(rt.Warnings, fmt.Sprintf("All %d pods run on one node (%s): losing it takes the whole workload down.", rt.Pods, worst))
	case rt.BlastRadius >= 50:
		rt.Warnings = append(rt.Warnings, fmt.Sprintf("%d of %d pods (%d%%) run on one node (%s): losing it takes most of the workload down.", rt.Nodes[0].Pods, rt.Pods, rt.BlastRadius, worst))
	}
	if rt.Pods >= 2 && len(rt.Nodes) > 1 && rt.Zones == 1 && maxZone == rt.Pods {
		rt.Warnings = append(rt.Warnings, fmt.Sprintf("All %d pods run in the same Availability Zone (%s).", rt.Pods, rt.Nodes[0].Zone))
	}
	if rt.SpotPods >= 2 && len(spotTypes) == 1 {
		for t := range spotTypes {
			rt.Warnings = append(rt.Warnings, fmt.Sprintf("%d pods run on Spot nodes that are all %s: one capacity shortage in that pool interrupts them together.", rt.SpotPods, orUnknown(t)))
		}
	}

	switch {
	case rt.Pods < 2, len(rt.Nodes) == 1, rt.Zones == 1 && maxZone == rt.Pods:
		rt.Resilience = "low"
	case rt.BlastRadius >= 50, rt.Zones > 1 && maxZone*100/rt.Pods >= 75:
		rt.Resilience = "medium"
	default:
		rt.Resilience = "high"
	}
	return rt
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "of the same instance type"
	}
	return s
}
