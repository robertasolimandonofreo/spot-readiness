<!-- spot-readiness -->
## Spot readiness

**3 workloads:** 1 ready, 0 with caveats, 2 not ready (1 skipped).

Spot potential: **27%** of requested CPU is in long-running workloads that are ready or ready with caveats (Jobs, CronJobs and ScaledJobs are not counted). [Estimate the savings](https://www.robertasoliman.com/en/tools/kubernetes-savings-calculator?tolerant=27).

| Verdict | Score | Confidence | Workload | Replicas | Findings |
|---|---|---|---|---|---|
| not-ready | 15 | low | `shop/statefulset/db` | 1 | SR001, SR006, SR007, SR009 |
| not-ready | 35 | high | `shop/deployment/worker` | 2 | SR003, SR004, SR005, SR006, SR009 |

### Karpenter NodePools

| NodePool | Capacity | AZs | Families | Types | Score | Findings |
|---|---|---|---|---|---|---|
| monitoring | on-demand | any | any | any | - |  |
| prod-app | spot | 1 | 2 | 4 | 45 | KR001, KR002, KR003, KR005, KR007 |
| workers | on-demand,spot | any | any | any | 100 |  |

<details><summary>Rules</summary>

- **SR001** (blocker): Fewer than 2 replicas (an HPA minimum counts), or a bare Pod
- **SR002** (blocker): hostPath volumes: the data is lost with the node
- **SR003** (caveat): terminationGracePeriodSeconds above 120s; Spot gives a 2-minute notice
- **SR004** (caveat): Several replicas with no topology spread or pod anti-affinity
- **SR005** (caveat): Several replicas with no matching PodDisruptionBudget
- **SR006** (caveat): A container without a readinessProbe
- **SR007** (caveat): Persistent volumes: zonal disks reschedule slower and stay in one zone
- **SR008** (info): A container without CPU or memory requests
- **SR009** (info): No preStop hook in any container
- **SR010** (blocker): Job with backoffLimit 0: a single lost pod fails the whole job
- **SR011** (info): Job with activeDeadlineSeconds: retries after an interruption count against the deadline
- **SR012** (info): Job without a podFailurePolicy that ignores DisruptionTarget: interruptions can count as failures
- **SR013** (info): Job progress is lost on interruption unless the job checkpoints (cannot be verified)
- **SR014** (blocker): PodDisruptionBudget allows zero voluntary disruptions: nodes cannot be drained
- **SR015** (caveat): PodDisruptionBudget without unhealthyPodEvictionPolicy AlwaysAllow: unhealthy pods can block a drain
- **SR016** (caveat): Workload is restricted to a single Availability Zone
- **SR017** (caveat): Workload requires a specific instance type (or node)
- **SR018** (info): Workload limits Spot capacity flexibility: few instance types or families, or GPUs
- **SR019** (caveat): Required nodeSelector and nodeAffinity pin down three or more aspects of the placement

- **KR001** (caveat): Spot NodePool supports only one Availability Zone
- **KR002** (caveat): Spot NodePool allows fewer than 3 instance families
- **KR003** (caveat): Spot NodePool allows fewer than 10 instance types
- **KR004** (caveat): Spot NodePool pins down 4 or more instance attributes
- **KR005** (info): Spot NodePool has no explicit disruption budget (the default is 10% of nodes), or a budget that blocks all disruption
- **KR006** (caveat): NodePool permits only Spot and no other NodePool can launch on-demand nodes
- **KR007** (info): Spot NodePool does not set minValues to enforce instance diversity

</details>

_Ready does not mean safe: this tool only sees configuration, not how your application behaves._
