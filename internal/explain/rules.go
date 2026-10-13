// Package explain documents every rule and shows how a workload got its score.
package explain

// Rule is the long form of one rule.
type Rule struct {
	ID       string
	Title    string
	Severity string
	// Why says what goes wrong on Spot, Detected when the tool reports it.
	Why      string
	Detected string
	// Bad and Good are YAML snippets; either can be empty when the fix is not a snippet.
	Bad  string
	Good string
}

// Rules are the rules in ID order: SR001 to SR019, then KR001 to KR007.
var Rules = []Rule{
	{"SR001", "Fewer than 2 replicas, or a bare Pod", "blocker",
		"With one replica, an interruption is downtime: AWS gives a 2-minute notice, and the replacement pod still has to be scheduled, maybe on a new node, pull its image and become ready. A bare Pod is worse, since nothing recreates it.",
		"The replica count (the HPA minReplicas, or the KEDA minReplicaCount, when one targets the workload) is below 2, or below spot.minReplicas in the config file. Also a Pod with no controller.",
		"spec:\n  replicas: 1", "spec:\n  replicas: 3   # or an HPA with minReplicas: 2"},
	{"SR002", "hostPath volume", "blocker",
		"A hostPath volume keeps its data on the node. When Spot reclaims the node the data goes with it, and the replacement pod starts on another node with an empty directory.",
		"The pod spec has a volume with hostPath.",
		"volumes:\n  - name: data\n    hostPath: {path: /var/lib/app}", "volumes:\n  - name: data\n    emptyDir: {}   # scratch data; use a PersistentVolumeClaim for data that must survive"},
	{"SR003", "terminationGracePeriodSeconds above 120 s", "caveat",
		"The AWS Spot interruption notice is two minutes. A shutdown that needs longer is cut off when the node is terminated, so connections drop and in-flight work is lost.",
		"terminationGracePeriodSeconds (30 s when unset) is above 120.",
		"terminationGracePeriodSeconds: 300", "terminationGracePeriodSeconds: 90   # and exit on SIGTERM inside that time"},
	{"SR004", "No topology spread or pod anti-affinity", "caveat",
		"Without spreading, the scheduler can put every replica on one node or in one zone, and a single Spot interruption removes them all. Spot capacity runs out per zone and instance type, so spread also protects against that.",
		"The workload has more than one replica and no topologySpreadConstraints and no podAntiAffinity (required or preferred).",
		"# no topologySpreadConstraints", "topologySpreadConstraints:\n  - maxSkew: 1\n    topologyKey: topology.kubernetes.io/zone\n    whenUnsatisfiable: ScheduleAnyway\n    labelSelector: {matchLabels: {app: api}}"},
	{"SR005", "No PodDisruptionBudget", "caveat",
		"When a node is drained (a Karpenter consolidation, a node upgrade, a reclaimed Spot node that is still draining) nothing limits how many replicas leave at once. A PodDisruptionBudget does not stop Spot from reclaiming a node: it only paces voluntary evictions.",
		"The workload has more than one replica and no PodDisruptionBudget in its namespace selects its pods.",
		"# no PodDisruptionBudget", "apiVersion: policy/v1\nkind: PodDisruptionBudget\nmetadata: {name: api}\nspec:\n  maxUnavailable: 1\n  unhealthyPodEvictionPolicy: AlwaysAllow\n  selector: {matchLabels: {app: api}}"},
	{"SR006", "Container without a readinessProbe", "caveat",
		"A new pod is added to the Service as soon as it starts. When a Spot node is replaced, traffic reaches pods that are not ready yet, and users see errors during every interruption.",
		"A container in the pod spec has no readinessProbe.",
		"containers:\n  - name: api\n    image: api:1", "containers:\n  - name: api\n    image: api:1\n    readinessProbe:\n      httpGet: {path: /healthz, port: 8080}\n      periodSeconds: 5"},
	{"SR007", "Persistent volumes", "caveat",
		"A volume backed by a zonal disk (EBS, for example) ties the pod to one zone. After an interruption the replacement can only start in that zone, which may have no Spot capacity, and the volume has to detach and attach again, which takes time.",
		"The pod spec mounts a PersistentVolumeClaim, or the StatefulSet has volumeClaimTemplates.",
		"volumes:\n  - name: data\n    persistentVolumeClaim: {claimName: data}", "# keep on on-demand nodes, or review replication and recovery before using Spot\nnodeSelector:\n  karpenter.sh/capacity-type: on-demand"},
	{"SR008", "Container without CPU or memory requests", "info",
		"Without requests the autoscaler cannot size nodes or bin-pack the pod, and pods with no requests are the first to be evicted under pressure.",
		"A container has no CPU request or no memory request.",
		"containers:\n  - name: api\n    image: api:1", "resources:\n  requests: {cpu: 250m, memory: 256Mi}"},
	{"SR009", "No preStop hook", "info",
		"When a pod is terminated, the endpoint removal and the SIGTERM happen at the same time, so for a moment the pod still receives requests it will not finish. A short preStop sleep lets the endpoint disappear first.",
		"No container in the pod spec has a lifecycle.preStop hook.",
		"containers:\n  - name: api\n    image: api:1", "lifecycle:\n  preStop:\n    exec: {command: [\"sleep\", \"15\"]}"},
	{"SR010", "Job with backoffLimit 0", "blocker",
		"A pod lost to a Spot interruption counts as a failure. With backoffLimit 0, one interruption fails the whole Job.",
		"A Job, CronJob or ScaledJob has backoffLimit set to 0.",
		"spec:\n  backoffLimit: 0", "spec:\n  backoffLimit: 6"},
	{"SR011", "Job with activeDeadlineSeconds", "info",
		"The deadline counts the time spent retrying after an interruption, so a job that is interrupted late can run out of time before it finishes.",
		"A Job, CronJob or ScaledJob sets activeDeadlineSeconds.",
		"spec:\n  activeDeadlineSeconds: 600", "spec:\n  activeDeadlineSeconds: 7200   # well above the normal duration, or remove it"},
	{"SR012", "Job without a podFailurePolicy that ignores DisruptionTarget", "info",
		"A pod lost to a node disruption counts against backoffLimit unless a podFailurePolicy ignores it, so interruptions can use up the retries.",
		"A Job, CronJob or ScaledJob has no podFailurePolicy rule with action Ignore on the DisruptionTarget condition.",
		"spec:\n  backoffLimit: 3", "spec:\n  podFailurePolicy:\n    rules:\n      - action: Ignore\n        onPodConditions: [{type: DisruptionTarget}]   # needs restartPolicy: Never"},
	{"SR013", "Job progress is lost unless it checkpoints", "info",
		"Whether a job saves its progress is behavior of the application. If it does not, an interruption restarts it from zero, which can cost hours.",
		"Always, for Jobs, CronJobs and ScaledJobs: the manifest cannot show it, so it is a reminder.",
		"", "# write progress to durable storage (S3, a PVC) and resume from it on start"},
	{"SR014", "PodDisruptionBudget allows zero voluntary disruptions", "blocker",
		"If the budget allows no eviction, a node holding the workload cannot be drained: a Karpenter consolidation, a node upgrade or the handling of an interruption notice waits on the budget until the node is gone anyway.",
		"A PodDisruptionBudget that selects the workload leaves no allowed disruption at the workload's minimum replica count (for example minAvailable equal to the replicas, or maxUnavailable 0). Percentages round up, like the disruption controller.",
		"spec:\n  minAvailable: 3   # with replicas: 3", "spec:\n  maxUnavailable: 1"},
	{"SR015", "PodDisruptionBudget without unhealthyPodEvictionPolicy AlwaysAllow", "caveat",
		"With the default policy a pod that is running but not ready can only be evicted while the budget is healthy, so a crash-looping or stuck pod can block a drain.",
		"A PodDisruptionBudget that selects the workload does not set unhealthyPodEvictionPolicy to AlwaysAllow.",
		"spec:\n  maxUnavailable: 1", "spec:\n  maxUnavailable: 1\n  unhealthyPodEvictionPolicy: AlwaysAllow"},
	{"SR016", "Restricted to a single Availability Zone", "caveat",
		"Spot capacity is pooled by zone and instance type. A workload that can only run in one zone loses all its capacity when that zone runs short.",
		"nodeSelector or required nodeAffinity limits the workload to fewer zones than spot.minZones (2 by default).",
		"nodeSelector:\n  topology.kubernetes.io/zone: us-east-1a", "# let it run in every zone, or list at least two\naffinity:\n  nodeAffinity:\n    requiredDuringSchedulingIgnoredDuringExecution:\n      nodeSelectorTerms:\n        - matchExpressions:\n            - {key: topology.kubernetes.io/zone, operator: In, values: [us-east-1a, us-east-1b, us-east-1c]}"},
	{"SR017", "Requires a specific instance type", "caveat",
		"One instance type is one Spot pool. When AWS has no capacity in it, or reclaims it, the workload has nowhere else to go.",
		"nodeSelector or required nodeAffinity names exactly one node.kubernetes.io/instance-type, or pins the pod to a node by kubernetes.io/hostname.",
		"nodeSelector:\n  node.kubernetes.io/instance-type: m5.large", "# allow a category and let Karpenter choose\naffinity:\n  nodeAffinity:\n    requiredDuringSchedulingIgnoredDuringExecution:\n      nodeSelectorTerms:\n        - matchExpressions:\n            - {key: karpenter.k8s.aws/instance-category, operator: In, values: [c, m, r]}"},
	{"SR018", "Limits Spot capacity flexibility", "info",
		"AWS recommends being flexible across at least 10 instance types for Spot. The fewer types a workload accepts, the fewer pools Karpenter can pick from, and the likelier a shortage.",
		"nodeSelector or required nodeAffinity lists 2 to 9 instance types, or 1 or 2 instance families, or the pod requests GPUs.",
		"- {key: node.kubernetes.io/instance-type, operator: In, values: [m5.large, m5.xlarge, c5.large]}", "# constrain by category and generation, not by type"},
	{"SR019", "Required placement pins down three or more aspects", "caveat",
		"Each required constraint (zone, instance type or family, architecture, a custom label) removes capacity pools. Together they can leave very few nodes that match.",
		"nodeSelector and required nodeAffinity together limit three or more of: zone, instance type, family, category, generation, architecture, GPU, hostname and custom labels.",
		"nodeSelector:\n  topology.kubernetes.io/zone: us-east-1a\n  kubernetes.io/arch: arm64\n  pool: gpu", "# keep only what the application needs (for example the architecture)"},
	{"KR001", "Spot NodePool supports only one Availability Zone", "caveat",
		"A NodePool limited to one zone has no Spot capacity left when that zone runs short, and pods stay Pending.",
		"The zone requirement of a NodePool that may launch Spot, narrowed to the zones of its EC2NodeClass subnets, leaves fewer than spot.minZones (2 by default).",
		"- {key: topology.kubernetes.io/zone, operator: In, values: [us-east-1a]}", "- {key: topology.kubernetes.io/zone, operator: In, values: [us-east-1a, us-east-1b, us-east-1c]}"},
	{"KR002", "Spot NodePool allows fewer than 3 instance families", "caveat",
		"Few families means few Spot pools to choose from when one runs short.",
		"The instance-family (or instance-type) requirements list fewer than 3 families. A pool limited only by category or size is not counted.",
		"- {key: karpenter.k8s.aws/instance-family, operator: In, values: [m5, c5]}", "- {key: karpenter.k8s.aws/instance-category, operator: In, values: [c, m, r]}\n- {key: karpenter.k8s.aws/instance-family, operator: Exists, minValues: 5}"},
	{"KR003", "Spot NodePool allows fewer than 10 instance types", "caveat",
		"AWS recommends at least 10 instance types for Spot, so Karpenter can move to another pool when one is reclaimed or runs short.",
		"The node.kubernetes.io/instance-type requirement lists fewer than 10 types.",
		"- {key: node.kubernetes.io/instance-type, operator: In, values: [m5.large, m5.xlarge]}", "- {key: karpenter.k8s.aws/instance-category, operator: In, values: [c, m, r]}\n- {key: karpenter.k8s.aws/instance-generation, operator: Gt, values: [\"2\"]}"},
	{"KR004", "Spot NodePool pins down 4 or more instance attributes", "caveat",
		"Category, family, generation, size, CPU, memory and architecture together can leave very few instance types that match.",
		"Four or more of those attributes are limited by a short list of values or a numeric bound.",
		"- {key: karpenter.k8s.aws/instance-category, operator: In, values: [c]}\n- {key: karpenter.k8s.aws/instance-generation, operator: In, values: [\"5\"]}\n- {key: karpenter.k8s.aws/instance-size, operator: In, values: [large]}\n- {key: kubernetes.io/arch, operator: In, values: [amd64]}", "# drop the constraints the workloads do not need"},
	{"KR005", "NodePool disruption budget", "info",
		"Without a budget Karpenter uses its default of 10% of nodes at a time. A budget of 0 nodes that is always active stops consolidation and drift from ever replacing a node. Spot interruptions ignore budgets: they only pace voluntary disruption.",
		"A NodePool that may launch Spot has no budget (info), or has a budget of 0 nodes with no schedule and no reasons (caveat).",
		"disruption:\n  budgets:\n    - nodes: \"0\"", "disruption:\n  budgets:\n    - nodes: \"10%\""},
	{"KR006", "NodePool permits only Spot and no other NodePool can launch on-demand", "caveat",
		"When Spot capacity runs out, pods that cannot fit stay Pending: there is no on-demand pool to fall back to.",
		"A NodePool allows only Spot, and no other NodePool allows on-demand (the default capacity type when none is set).",
		"- {key: karpenter.sh/capacity-type, operator: In, values: [spot]}", "- {key: karpenter.sh/capacity-type, operator: In, values: [spot, on-demand]}   # Karpenter prefers Spot"},
	{"KR007", "Spot NodePool without minValues", "info",
		"minValues makes Karpenter keep a minimum number of distinct instance types, families or categories when it launches nodes, which keeps the pool diverse.",
		"A NodePool that may launch Spot has no minValues on instance type, family or category.",
		"- {key: karpenter.k8s.aws/instance-family, operator: Exists}", "- {key: karpenter.k8s.aws/instance-family, operator: Exists, minValues: 5}"},
}

// Find returns the rule with the ID.
func Find(id string) (Rule, bool) {
	for _, r := range Rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}
