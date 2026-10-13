# spot-readiness

Tells you which Kubernetes workloads can run on Spot capacity, and what to fix in the ones that cannot.

"Use Spot for interruption-tolerant workloads" is common advice. Finding out which workloads those
are is usually a guess. `spot-readiness` reads your Deployments, StatefulSets, Argo Rollouts, Jobs, CronJobs, KEDA ScaledJobs
and Pods and checks the things that decide whether an interruption is a non-event or an outage.

> **Status: early.** It scans YAML/JSON manifests, or reads a live cluster (read-only). It is a static
> analysis of configuration, not a guarantee: see [Limits](#limits).

## Install

Download a binary for Linux, macOS or Windows (amd64 and arm64) from the
[releases page](https://github.com/robertasolimandonofreo/spot-readiness/releases), and check it
against `checksums.txt`:

```bash
tar -xzf spot-readiness_<version>_linux_amd64.tar.gz
sha256sum -c --ignore-missing checksums.txt
sudo mv spot-readiness /usr/local/bin/
```

Or use the container image (linux/amd64 and linux/arm64):

```bash
docker run --rm -v "$PWD:/work:ro" ghcr.io/robertasolimandonofreo/spot-readiness:latest scan /work
```

The image only reads the files you mount. Cluster mode needs a kubeconfig that works without helper
programs: kubeconfigs that call `aws eks get-token` or `gcloud` do not work inside the image, so
use the binary for those.

Or build it with Go:

```bash
go install github.com/robertasolimandonofreo/spot-readiness/cmd/spot-readiness@latest
```

## Use

```bash
spot-readiness scan ./k8s                      # a directory of manifests
spot-readiness scan -v deployment.yaml         # explain every finding
helm template my-release ./chart | spot-readiness scan -
kustomize build overlays/prod | spot-readiness scan -f json -
spot-readiness scan -f csv ./k8s > workloads.csv
spot-readiness scan --fail-on not-ready ./k8s  # exit 1 in CI
```

### Reading a cluster

With no paths, `scan` reads the cluster from your kubeconfig. It only uses `list` calls.

```bash
spot-readiness scan                          # current context, all namespaces
spot-readiness scan --context prod -n shop   # a specific context and namespace
spot-readiness scan --kubeconfig ./kc.yaml -f csv
```

The minimum RBAC is `list` on `deployments`, `statefulsets`, `daemonsets` (apps), `pods` (core),
`jobs` and `cronjobs` (batch), `poddisruptionbudgets` (policy) and `horizontalpodautoscalers`
(autoscaling). `nodes` (core) and `replicasets` (apps) are optional and enable the runtime risk view. `--karpenter` also needs `list` on `nodepools`, `nodeclaims` (karpenter.sh) and `ec2nodeclasses` (karpenter.k8s.aws). Argo Rollouts (`rollouts`) and KEDA (`scaledobjects`, `scaledjobs`) are read too when
those CRDs exist. Everything except Deployments, StatefulSets and Pods is optional: a missing permission or
CRD only means that part is not analyzed. The tool never creates, changes or deletes anything and sends nothing outside your machine.

Example output:

```
VERDICT             SCORE  WORKLOAD                REPLICAS  CPU    SPOT  FINDINGS
not-ready           15     shop/statefulset/db     1         2000m  -     SR001,SR006,SR007,SR009
not-ready           35     shop/deployment/worker  2         2000m  -     SR003,SR004,SR005,SR006,SR009
ready               100    shop/deployment/web     3         1500m  -

3 workloads: 1 ready, 0 with caveats, 2 not ready
Spot potential: 27% of requested CPU (workloads ready or with caveats).
```

`-f`/`--format` accepts `table` (default), `json`, `csv` and `markdown` or `md` (`-o`/`--output` are
aliases). CSV has one
row per workload with the columns `verdict, score, namespace, kind, name, replicas,
cpu_request_millis, already_on_spot, findings` (rule IDs joined with `;`) and no summary.

### Pull request comments

`-f markdown` prints a report for a pull request comment: a summary, a table of the workloads that
need attention (worst first, at most 50 rows) and the rule reference in a collapsed block. The first
line is the marker `<!-- spot-readiness -->`, so a CI step can update its own comment. The
[GitHub Action](#github-action) below posts and updates that comment for you.

### Exit codes and skipped objects

`--fail-on` accepts `not-ready` or `caveats`. DaemonSets and ReplicaSets are counted as skipped, and
so are workloads scaled to zero (without an HPA or KEDA minimum), suspended CronJobs, finished Jobs
(in a cluster), Jobs created by a CronJob (the CronJob is analyzed instead), capacity placeholders
(workloads that only run `pause` containers, for example to make Karpenter create nodes ahead of
time), Rollouts that use `workloadRef` and objects with no name, such as unrendered Helm templates.
`kube-system`, `kube-public` and `kube-node-lease` are skipped unless you pass `--include-system`.

## GitHub Action

The repository is also a GitHub Action. It installs the release that matches the action's version
(checking its checksum), scans your manifests, adds the report to the job summary and, optionally,
comments on the pull request and fails the job.

```yaml
name: spot-readiness
on: pull_request
permissions:
  contents: read
  pull-requests: write   # only needed when comment is true
jobs:
  scan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - id: spot
        uses: robertasolimandonofreo/spot-readiness@v0.6.0
        with:
          path: ./k8s            # files or directories, separated by spaces
          comment: "true"        # one comment, updated on every push
          fail-on: not-ready     # or caveats; empty never fails
      - run: echo "Spot potential ${{ steps.spot.outputs.potential-percent }}%"
```

| Input | Default | |
|---|---|---|
| `path` | `.` | Files or directories to scan, separated by spaces |
| `comment` | `false` | Comment on the pull request and keep that comment updated |
| `fail-on` | empty | `not-ready` or `caveats` fails the job; the comment is still posted |
| `fail-on-regression` | `false` | Fail when the pull request adds a blocker or caveat finding that the base branch did not have |
| `sarif` | empty | Also write a SARIF report to this file, to upload to Code Scanning |
| `config` | empty | Config file; defaults to `.spot-readiness.yaml` in the repository root |
| `include-system` | `false` | Also analyze `kube-system` and the other system namespaces |
| `version` | the action's version | A release such as `v0.6.0`; with a branch ref it uses the latest release |
| `github-token` | `github.token` | Token used to comment |

Outputs: `potential-percent`, `ready`, `caveats` and `not-ready`. It runs on Linux and macOS runners.
The action reads manifests only; for a live cluster use the binary or the image. On pull requests
from forks `GITHUB_TOKEN` is read-only, so `comment` fails there.

## Rules

Each workload starts at 100 points. A blocker costs 50, a caveat 15 and an info 5. Any blocker makes
the verdict `not-ready`. A workload with caveats is `ready-with-caveats` only while it keeps at
least 50 points.

| Rule | Severity | Checks |
|---|---|---|
| SR001 | blocker | Fewer than 2 replicas (an HPA `minReplicas` counts), or a bare Pod |
| SR002 | blocker | `hostPath` volumes |
| SR003 | caveat | `terminationGracePeriodSeconds` above 120s (Spot gives a 2-minute notice) |
| SR004 | caveat | Several replicas with no topology spread constraint or pod anti-affinity |
| SR005 | caveat | Several replicas with no matching PodDisruptionBudget |
| SR006 | caveat | A container without a `readinessProbe` |
| SR007 | caveat | Persistent volumes (zonal disks reschedule slower and stay in one zone) |
| SR008 | info | A container without CPU or memory requests |
| SR009 | info | No `preStop` hook in any container |
| SR010 | blocker | Job with `backoffLimit: 0`: one lost pod fails the whole job |
| SR011 | info | Job with `activeDeadlineSeconds`: retries after an interruption count against it |
| SR012 | info | No `podFailurePolicy` that ignores `DisruptionTarget`: an interruption can count against `backoffLimit` |
| SR013 | info | Progress is lost on interruption unless the job checkpoints (cannot be verified) |
| SR014 | blocker | A matching PodDisruptionBudget allows zero voluntary disruptions (for example `minAvailable` equal to the replicas): nodes cannot be drained |
| SR015 | caveat | A matching PodDisruptionBudget without `unhealthyPodEvictionPolicy: AlwaysAllow`: a running but not ready pod can block a drain |
| SR016 | caveat | Restricted to a single Availability Zone by `nodeSelector` or required `nodeAffinity` |
| SR017 | caveat | Requires one specific instance type, or pins to nodes by `kubernetes.io/hostname` |
| SR018 | info | Limits Spot capacity flexibility: 2 to 9 instance types, 1 or 2 instance families, or GPUs |
| SR019 | caveat | Required `nodeSelector` and `nodeAffinity` pin down three or more aspects of the placement (zone, instance, architecture, custom labels) |

SR001, SR004, SR005, SR006, SR009, SR014 and SR015 apply to long-running workloads (Deployments, StatefulSets,
Argo Rollouts and bare Pods). SR010 to SR013 apply to Jobs, CronJobs and KEDA ScaledJobs. SR002, SR003, SR007, SR008 and
SR016 to SR019 apply to all of them.

### Interruption handling (cluster mode)

Workloads that are ready for Spot still lose their node if nothing reacts to the 2-minute notice.
In cluster mode (all namespaces) the report ends with an `INTERRUPTION HANDLING` section, and
`--interruption` turns it on for manifests too. It shows whether Karpenter is running, whether its
controller has an interruption queue (the `INTERRUPTION_QUEUE` variable or `--interruption-queue`
flag), whether Spot nodes exist, whether an AWS Node Termination Handler runs, how many workloads
have PodDisruptionBudget problems (SR005, SR014) and how many need more than 120s to terminate
(SR003). Spot nodes are counted by what launched them, from node labels: Karpenter nodes need the
queue or the handler, nodes outside Karpenter and EKS managed node groups need the handler, and EKS
managed node groups drain Spot nodes themselves (capacity rebalancing).

The cluster interruption readiness score starts at 100 and loses 40 when nothing handles the notice,
5 when the Karpenter queue and the handler both run (Karpenter's documentation recommends one), 5 per
workload with a PodDisruptionBudget problem (up to 25) and 4 per workload with a slow termination (up
to 16). Without Spot capacity there is no score. It is in the JSON `interruption` object and does not
change `--fail-on`. It is skipped with `--namespace`, because Karpenter and the handler live in other
namespaces. It cannot see an interruption queue configured in the older `karpenter-global-settings`
ConfigMap, or how an Auto Scaling group is set up in AWS.

### Karpenter NodePools (`--karpenter`)

`scan --karpenter` also checks Karpenter NodePools: in cluster mode it reads `nodepools`,
`nodeclaims` and `ec2nodeclasses` (karpenter.sh and karpenter.k8s.aws, `v1` or `v1beta1`); with paths it reads
those kinds from the manifests. These rules use the `KR` prefix because they judge the platform that
provides Spot nodes, not an application. They apply only to pools that may launch Spot (Karpenter
launches on-demand nodes unless `karpenter.sh/capacity-type` says otherwise); other pools are listed
without a score. Each pool starts at 100 and loses 15 per caveat and 5 per info.

| Rule | Severity | Checks |
|---|---|---|
| KR001 | caveat | Only one Availability Zone, from the `topology.kubernetes.io/zone` requirement narrowed to the zones of the EC2NodeClass subnets (read from its status) |
| KR002 | caveat | Fewer than 3 instance families, listed with `instance-family` or `instance-type` |
| KR003 | caveat | Fewer than 10 instance types listed with `node.kubernetes.io/instance-type` |
| KR004 | caveat | Four or more instance attributes pinned (category, family, generation, size, CPU, memory, architecture, type) |
| KR005 | info or caveat | No explicit disruption budget (Karpenter then uses 10% of nodes), or an always-active budget of 0 nodes (caveat) |
| KR006 | caveat | Only Spot is allowed and no other NodePool can launch on-demand nodes |
| KR007 | info | No `minValues` on instance type, family or category |

The table shows the zones, families and types the NodePool lists, and the nodes (NodeClaims) it runs
now. Karpenter picks from the full EC2 catalog, so a pool that limits only by category or size shows
`any`: this tool has no instance catalog, and `minValues` is what makes Karpenter enforce a minimum
there. Spot interruptions ignore disruption budgets, which only rate-limit voluntary disruption. The
analysis shows in the table, the JSON `karpenter` list and the Markdown report, and does not change
`--fail-on`.

### Runtime risk (cluster mode)

The rules above read configuration. When the tool reads a cluster it also checks where the running
pods are: it follows each pod to its node and reads the node's zone, capacity type
(Karpenter or EKS managed node group labels) and instance type. The `RUNTIME RISK` section lists
Deployments, StatefulSets, Argo Rollouts and bare Pods whose pods are not well spread, with the blast
radius of one node (the share of pods on the busiest node), a warning for each problem and the
runtime resilience (`low`, `medium` or `high`) next to the manifest score. `-v` adds the table of
nodes. It warns when all pods share one node or one Availability Zone, when half or more are on one
node, and when all Spot pods sit in one instance type pool. The JSON `runtime` object and the CSV
`runtime_resilience` column carry the same data.

The runtime view is a snapshot and does not change the score or the verdict. It is skipped without
permission to list `nodes`, and Jobs are not covered. Capacity types are read from AWS labels only.

### Spot flexibility

Surviving a restart is not enough: Spot works best when a workload can move between many capacity
pools (zone, instance type, family). Each workload also gets a flexibility score from 100 (it can
run anywhere) down to 0, shown in the `SPOT FLEXIBILITY` table (workloads with no placement
constraints are left out), in the JSON `flexibility` object and in the CSV `flexibility_score`
column. It is separate from the readiness score; only SR016 to SR019 affect that one.

It reads `nodeSelector` and required `nodeAffinity` (alternative terms are combined, so a workload
that may run in three zones counts as three), for zones, instance type, family, category,
generation, architecture, GPU labels and custom labels, plus GPU resource requests. Constraints
are `low` (80 and above), `medium` (50 to 79) or `high`. The score and the instance type
threshold of 10 follow AWS's advice to be flexible across many instance types on Spot. It does not
read `tolerations`, `topologySpreadConstraints` or preferred affinity, which do not narrow where a
pod can run, and it cannot see the instance types a Karpenter NodePool or node group allows.

The "Spot potential" line is the share of requested CPU in long-running workloads that are ready or
ready with caveats. Jobs, CronJobs and ScaledJobs are not counted: they request CPU only while they run, so
weighing them like always-on services would overstate their share. The link in the output opens the
[Kubernetes savings calculator](https://www.robertasoliman.com/en/tools/kubernetes-savings-calculator)
with that number filled in as the interruption-tolerant share.

## Migration plan (`plan`)

`spot-readiness plan [paths]` turns the analysis into an order of work. It takes the same input as
`scan` (paths, or the cluster when there are none), the same `--config`, `--include-system`,
`--karpenter` and `--interruption` flags, and `-f table`, `json` or `markdown`. `--suggest` adds the
snippet of every change.

```
SPOT MIGRATION PLAN

Total workloads:             42
Ready now:                   18
Ready after changes:         15
Keep on on-demand:           9

Requested CPU:               84 vCPU
Immediately movable to Spot: 37 vCPU (44%)
Potential after changes:     66 vCPU (79%)

Phase 1: move now (safest first)
  shop/deployment/api  (4 vCPU)
  ...
Phase 2: move after these changes (fewest changes first)
  shop/deployment/checkout  (8 vCPU)
    SR004  Spread the replicas across zones and nodes.
    SR006  Add a readinessProbe so traffic only reaches pods that can serve it.
Keep on on-demand
  shop/statefulset/postgres  (6 vCPU)
    uses persistent volumes: zonal disks make rescheduling slower and tied to one zone
```

Workloads fall in three groups:

- **Ready now** (phase 1): no blocker and no caveat. Info findings are advice, not a condition.
  Listed safest first (highest score, then the most CPU).
- **Ready after changes** (phase 2): blockers or caveats, but none that configuration cannot fix.
  Each change is the first line of the finding's suggested fix. Listed with the fewest changes first.
- **Keep on on-demand**: data that lives on the node (SR002, `hostPath`) or a StatefulSet with
  persistent volumes (SR007). Moving those is a design decision, not a configuration fix.

The CPU figures cover long-running workloads, like the Spot potential of `scan`: Jobs and CronJobs
are listed in the phases but not counted. When the cluster was read (or with `--interruption`),
phase 0 lists what the platform needs before the first move: a Karpenter interruption queue or a
Node Termination Handler (see Interruption handling), and, with `--karpenter`, the NodePool
caveats. The groups and the order are heuristics from the manifests: they do not know how your
application behaves, how much a move saves in money, or which teams own what.

## Suggested fixes (`--suggest`)

Every finding carries a suggested fix and a link to the documentation. `scan --suggest` prints them
under each finding (it implies `-v`), with the names of your workload filled in:

```
shop/deployment/api
  SR004 [caveat] no topology spread or pod anti-affinity: ... (k8s/api.yaml:39)
    Suggested remediation:
      Spread the replicas across zones and nodes.

      # spec.template.spec
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              app: api
        ...
    Docs: https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/
```

The first line is the advice, then a snippet to adapt, headed by where it goes (the `#` line).
Values such as a health path, a grace period or a list of zones are starting points, not
measurements: check them against your application. The tool never changes your files. The JSON
findings have `remediation` and `docsUrl` (the Karpenter findings too), and the SARIF rules link
to the documentation. Karpenter findings are shown with `--karpenter --suggest`.

## SARIF and Code Scanning (`-f sarif`)

`scan -f sarif ./k8s` writes a SARIF 2.1.0 report that GitHub Code Scanning shows on the manifest
line, in the pull request's Files changed tab:

```
k8s/deployment.yaml:42   SR006   container "api" has no readinessProbe
```

Each finding points to the field it is about: the container for SR006 and SR008, `replicas` for
SR001, `terminationGracePeriodSeconds` for SR003, `backoffLimit` for SR010, the node selector or
affinity for SR016 to SR019. A finding about a setting that is missing from the manifest (no
`preStop`, no topology spread) points to the nearest part that exists, such as the pod `spec`, and
one that is not about a field (SR005, a missing PodDisruptionBudget) to the object's `name`. Blockers
are errors, caveats warnings and infos notes. Paths are relative to the directory where you run the
scan, so run it from the repository root. The alert fingerprint ignores the line number, so editing the
file does not reopen a dismissed alert. Findings from a cluster scan have no file, and the Karpenter
(`KR`) rules are not included. `-v` also prints `file:line` for each finding, and the JSON findings have
`file` and `line`.

```yaml
permissions:
  contents: read
  pull-requests: write
  security-events: write
steps:
  - uses: actions/checkout@v4
  - uses: robertasolimandonofreo/spot-readiness@main
    with:
      path: k8s
      sarif: spot-readiness.sarif
  - uses: github/codeql-action/upload-sarif@v3
    if: always()
    with:
      sarif_file: spot-readiness.sarif
```

## Regressions (`--baseline`)

A project that already has 30 findings cannot fail its build on them, but it can refuse new ones.
`scan --baseline report.json` compares the scan with an earlier `-f json` report and adds the
average workload score of both, the findings that are new and the ones that were resolved (a finding
is a rule on a workload). `--fail-on-regression` exits 1 when a new finding is a blocker or a
caveat; new info findings are shown but never fail the build.

```bash
spot-readiness scan -f json ./k8s > baseline.json          # on the base branch
spot-readiness scan --baseline baseline.json --fail-on-regression ./k8s
```

```
COMPARISON WITH BASELINE

Baseline: 87
Current:  78
Change:   -9

NEW FINDINGS

+ SR004 [caveat] shop/deployment/api-prod

RESOLVED

- SR005 [caveat] shop/deployment/checkout-api
```

The GitHub Action does this on its own for `pull_request` events: it scans the base commit in a
separate worktree (with the pull request's config, so changing the config cannot fake an improvement),
adds the comparison to the comment and, with `fail-on-regression: "true"`, fails the job after
posting it. The checkout does not need full history, since the action fetches the base commit. A path
that did not exist on the base branch counts as empty, so everything in it is new. If the base cannot
be scanned, the action warns and skips the comparison, except with `fail-on-regression`, which then
fails rather than pass without checking. A workload that is renamed shows as new and resolved.

## Configuration (`.spot-readiness.yaml`)

A team can adopt the tool without accepting every heuristic. The tool reads `.spot-readiness.yaml`
(or `.yml`) from the current directory, or the file given with `--config`. Unknown fields and rule
IDs are errors, so a typo does not silently disable a setting.

```yaml
version: 1

thresholds:
  minimumScore: 70        # exit 1 when a workload scores below this
  failOn: not-ready       # not-ready or caveats, like --fail-on (the flag wins)

exclude:
  namespaces:             # skipped entirely, even with --include-system
    - monitoring

rules:
  SR007:
    severity: info        # blocker, caveat, info or off; also works for KR rules
  SR009:
    severity: "off"

ignore:
  - workload: database/postgres   # namespace/name; * matches within a part ("shop/*")
    kind: StatefulSet             # optional
    rules: [SR007]                # empty means every rule
    reason: "Stateful workload intentionally kept on on-demand"

spot:
  minReplicas: 2          # SR001 fires below this (default 2)
  minZones: 2             # SR016 and KR001 fire below this (default 2)
```

Overridden severities change the penalty and the verdict. Ignored findings do not count, but they
stay visible: `-v` lists them with their reason, the JSON has an `ignored` list per workload and a
`summary.ignoredFindings` count. The `reason` is optional, though it is what makes an ignore
reviewable. The GitHub Action reads the file from the repository root (or its `config` input),
posts the comment and then fails the job when a threshold is not met. Ignore entries apply to
workloads, not to Karpenter NodePools: use `rules` to change or turn off a KR rule.

## JSON

`-f json` prints a `summary` and a `results` list with each workload's verdict, score and findings.

## Limits

- **Jobs.** The tool cannot see whether a job checkpoints its progress, so a "ready" Job can still
  lose hours of work when it is interrupted (SR013 reminds you of this).
- **Argo Rollouts and KEDA.** A Rollout that takes its pod template from a Deployment
  (`workloadRef`) is skipped, and that Deployment is analyzed instead. A KEDA ScaledObject sets the
  minimum replica count (its default is 0), the same way an HPA does.
- **Ready does not mean safe.** The tool only sees configuration. It cannot know whether your
  application handles `SIGTERM`, keeps state in memory or tolerates a restart.
- Rules are heuristics. A workload can pass every rule and still be a bad fit for Spot, or fail a
  rule for a good reason. Read the findings, do not just follow the score.
- A PodDisruptionBudget does not stop Spot reclaims. It only limits how many replicas a drain
  removes at once.
- Persistent-volume and local-storage checks look at the pod spec. A `local` PersistentVolume that
  is only visible in the cluster is not detected.

## Roadmap

Nothing is planned. If asked: Argo Rollouts that use `workloadRef` (today the referenced
Deployment is analyzed).

Checking whether a Job checkpoints its progress is not on the roadmap: it is behavior of the
application, and manifests do not show it.

## Development

```bash
go test ./...
go run ./cmd/spot-readiness scan testdata
```

Rules live in `internal/rules/rules.go` as small functions and are covered by table tests. A new
rule needs an ID, a severity, a check and a test case.

## License

Apache-2.0. See [LICENSE](LICENSE).
