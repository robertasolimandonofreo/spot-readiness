package rules

import (
	"fmt"
	"sort"
	"strings"
)

// docsURL is the documentation to read for each rule.
var docsURL = map[string]string{
	"SR001": "https://kubernetes.io/docs/concepts/workloads/controllers/deployment/",
	"SR002": "https://kubernetes.io/docs/concepts/storage/volumes/",
	"SR003": "https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/",
	"SR004": "https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/",
	"SR005": "https://kubernetes.io/docs/tasks/run-application/configure-pdb/",
	"SR006": "https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/",
	"SR007": "https://kubernetes.io/docs/concepts/storage/persistent-volumes/",
	"SR008": "https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/",
	"SR009": "https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/",
	"SR010": "https://kubernetes.io/docs/concepts/workloads/controllers/job/",
	"SR011": "https://kubernetes.io/docs/concepts/workloads/controllers/job/",
	"SR012": "https://kubernetes.io/docs/concepts/workloads/controllers/job/",
	"SR013": "https://kubernetes.io/docs/concepts/workloads/controllers/job/",
	"SR014": "https://kubernetes.io/docs/tasks/run-application/configure-pdb/",
	"SR015": "https://kubernetes.io/docs/tasks/run-application/configure-pdb/",
	"SR016": "https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-best-practices.html",
	"SR017": "https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-best-practices.html",
	"SR018": "https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-best-practices.html",
	"SR019": "https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/",
}

// DocsURL is the documentation to read for a rule.
func DocsURL(rule string) string { return docsURL[rule] }

// selectorLabels are the labels a selector for the workload's pods can use.
func selectorLabels(w Workload) []string {
	var out []string
	for k, v := range w.Template.Labels {
		out = append(out, fmt.Sprintf("%s: %s", k, v))
	}
	sort.Strings(out)
	if len(out) == 0 {
		out = []string{"app: " + w.Name}
	}
	return out
}

func indent(lines []string, by string) string {
	for i := range lines {
		lines[i] = by + lines[i]
	}
	return strings.Join(lines, "\n")
}

func firstContainer(w Workload) string {
	if cs := containers(w); len(cs) > 0 {
		return cs[0].Name
	}
	return "app"
}

// containerPort is the first declared port of the container, or 8080.
func containerPort(w Workload, name string) int32 {
	for _, c := range containers(w) {
		if c.Name == name && len(c.Ports) > 0 {
			return c.Ports[0].ContainerPort
		}
	}
	return 8080
}

// containerOf reads the container name out of a "pod.containers[name]" path.
func containerOf(path string) string {
	if i := strings.Index(path, "["); i >= 0 && strings.HasSuffix(path, "]") {
		return path[i+1 : len(path)-1]
	}
	return ""
}

// blockingPDB is the first PodDisruptionBudget that selects the workload and allows no disruption.
func blockingPDB(inv *Inventory, w Workload) string {
	replicas := inv.MinReplicas(w)
	for _, p := range inv.MatchingPDBs(w) {
		if blocksAllEvictions(p, replicas) {
			return p.Name
		}
	}
	return "<name>"
}

func firstPDB(inv *Inventory, w Workload) string {
	for _, p := range inv.MatchingPDBs(w) {
		if pol := p.Spec.UnhealthyPodEvictionPolicy; pol == nil || *pol != "AlwaysAllow" {
			return p.Name
		}
	}
	return "<name>"
}

// remediate fills in the suggested fix and the documentation link of a finding. The first line is
// the advice, the lines after it a snippet to adapt, with the workload's own names where known.
func remediate(f *Finding, inv *Inventory, w Workload) {
	f.DocsURL = docsURL[f.Rule]
	pod := PodSpecPath(w.Kind)
	job := JobSpecPath(w.Kind)
	switch f.Rule {
	case "SR001":
		if w.Kind == "Pod" {
			f.Remediation = "Run the pod from a Deployment (or StatefulSet) with at least 2 replicas, so something recreates it."
			break
		}
		f.Remediation = "Run at least 2 replicas (3 across zones is better), or give the HPA minReplicas: 2.\n\n# " + w.Kind + ": spec\nreplicas: 2"
	case "SR002":
		name := "<volume>"
		for _, v := range w.Template.Spec.Volumes {
			if v.HostPath != nil {
				name = v.Name
				break
			}
		}
		f.Remediation = "Use emptyDir for scratch data, or a PersistentVolumeClaim for data that must outlive the node.\n\n# " + pod + ".volumes\n- name: " + name + "\n  emptyDir: {}"
	case "SR003":
		f.Remediation = "Make the application exit on SIGTERM within the 2-minute Spot notice and lower the grace period.\n\n# " + pod + "\nterminationGracePeriodSeconds: 90"
	case "SR004":
		sel := indent(selectorLabels(w), "        ")
		f.Remediation = "Spread the replicas across zones and nodes.\n\n# " + pod + "\ntopologySpreadConstraints:\n" +
			"  - maxSkew: 1\n    topologyKey: topology.kubernetes.io/zone\n    whenUnsatisfiable: ScheduleAnyway\n    labelSelector:\n      matchLabels:\n" + sel + "\n" +
			"  - maxSkew: 1\n    topologyKey: kubernetes.io/hostname\n    whenUnsatisfiable: ScheduleAnyway\n    labelSelector:\n      matchLabels:\n" + sel
	case "SR005":
		sel := indent(selectorLabels(w), "      ")
		f.Remediation = "Add a PodDisruptionBudget so a drain removes one replica at a time.\n\napiVersion: policy/v1\nkind: PodDisruptionBudget\nmetadata:\n  name: " + w.Name +
			"\n  namespace: " + w.Namespace + "\nspec:\n  maxUnavailable: 1\n  unhealthyPodEvictionPolicy: AlwaysAllow\n  selector:\n    matchLabels:\n" + sel
	case "SR006":
		name := containerOf(f.Path)
		f.Remediation = fmt.Sprintf("Add a readinessProbe so traffic only reaches pods that can serve it. Point it at your health endpoint.\n\n# %s.containers[%s]\nreadinessProbe:\n  httpGet:\n    path: /healthz\n    port: %d\n  periodSeconds: 5\n  failureThreshold: 3",
			pod, name, containerPort(w, name))
	case "SR007":
		f.Remediation = "A zonal volume keeps the pod in one zone and slows rescheduling. Keep this workload on on-demand nodes, or accept slower recovery.\n\n# " + pod + "\nnodeSelector:\n  karpenter.sh/capacity-type: on-demand"
	case "SR008":
		name := containerOf(f.Path)
		f.Remediation = "Set CPU and memory requests so the autoscaler can bin-pack the pod. Base them on real usage.\n\n# " + pod + ".containers[" + name + "]\nresources:\n  requests:\n    cpu: 250m\n    memory: 256Mi"
	case "SR009":
		f.Remediation = "Add a short preStop sleep so the endpoint is removed from load balancers before the process gets SIGTERM.\n\n# " + pod + ".containers[" + firstContainer(w) + "]\nlifecycle:\n  preStop:\n    exec:\n      command: [\"sleep\", \"15\"]"
	case "SR010":
		f.Remediation = "Let the job retry: with backoffLimit 0 one lost pod fails it.\n\n# " + job + "\nbackoffLimit: 6"
	case "SR011":
		f.Remediation = "Raise the deadline well above the job's normal duration, or remove it, because retries after an interruption count against it.\n\n# " + job + "\nactiveDeadlineSeconds: 7200"
	case "SR012":
		f.Remediation = "Do not count pods lost to a node disruption against backoffLimit (needs restartPolicy: Never).\n\n# " + job + "\npodFailurePolicy:\n  rules:\n    - action: Ignore\n      onPodConditions:\n        - type: DisruptionTarget"
	case "SR013":
		f.Remediation = "Write progress to durable storage (S3, a PVC) and resume from it when the job starts, so an interruption does not restart from zero."
	case "SR014":
		name := blockingPDB(inv, w)
		f.Remediation = "Allow at least one disruption, or drains and node replacement hang. Prefer maxUnavailable over a minAvailable equal to the replicas.\n\n# PodDisruptionBudget " + name + "\nspec:\n  maxUnavailable: 1"
	case "SR015":
		name := firstPDB(inv, w)
		f.Remediation = "Let a drain evict pods that are running but not ready, so they cannot block it.\n\n# PodDisruptionBudget " + name + "\nspec:\n  unhealthyPodEvictionPolicy: AlwaysAllow"
	case "SR016":
		f.Remediation = "Let the workload run in every zone of the cluster, or drop the zone constraint.\n\n# " + pod + "\naffinity:\n  nodeAffinity:\n    requiredDuringSchedulingIgnoredDuringExecution:\n      nodeSelectorTerms:\n        - matchExpressions:\n            - key: topology.kubernetes.io/zone\n              operator: In\n              values: [\"<zone-a>\", \"<zone-b>\", \"<zone-c>\"]"
	case "SR017":
		f.Remediation = "Allow a category or several families instead of one instance type, so Karpenter has many Spot pools to choose from.\n\n# " + pod + "\naffinity:\n  nodeAffinity:\n    requiredDuringSchedulingIgnoredDuringExecution:\n      nodeSelectorTerms:\n        - matchExpressions:\n            - key: karpenter.k8s.aws/instance-category\n              operator: In\n              values: [\"c\", \"m\", \"r\"]"
	case "SR018":
		f.Remediation = "Allow more instance types: AWS recommends being flexible across at least 10 types for Spot. Let the NodePool choose, and keep only the constraints the application needs (for example a GPU)."
	case "SR019":
		f.Remediation = "Keep only the node constraints the application really needs (for example the architecture) and let the NodePool decide the rest: every extra constraint removes Spot pools."
	}
}
