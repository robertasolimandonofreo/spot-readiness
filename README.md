# spot-readiness

Tells you which Kubernetes workloads can run on Spot capacity, and what to fix in the ones that cannot.

"Use Spot for interruption-tolerant workloads" is common advice. Finding out which workloads those
are is usually a guess. `spot-readiness` reads your Deployments, StatefulSets, Argo Rollouts, Jobs, CronJobs and Pods and
checks the things that decide whether an interruption is a non-event or an outage.

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
(autoscaling). Argo Rollouts (`rollouts`) and KEDA (`scaledobjects`) are read too when those CRDs
exist. Everything except Deployments, StatefulSets and Pods is optional: a missing permission or
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
line is the marker `<!-- spot-readiness -->`, so a CI step can update its own comment.

```yaml
# Example GitHub Actions job (adapt it to your repository)
- run: |
    docker run --rm -v "$PWD:/work:ro" ghcr.io/robertasolimandonofreo/spot-readiness:latest \
      scan -f markdown /work/k8s > report.md
    gh pr comment ${{ github.event.pull_request.number }} --body-file report.md --edit-last --create-if-none
  env:
    GH_TOKEN: ${{ github.token }}
```

On pull requests from forks, GitHub gives `GITHUB_TOKEN` read-only access, so the comment step
fails there. Use it for branches in the same repository, or post the report from a separate
`workflow_run` job.

`--fail-on` accepts `not-ready` or `caveats`. DaemonSets and ReplicaSets are counted as skipped, and
so are workloads scaled to zero (without an HPA or KEDA minimum), suspended CronJobs, finished Jobs
(in a cluster), Jobs created by a CronJob (the CronJob is analyzed instead), capacity placeholders
(workloads that only run `pause` containers, for example to make Karpenter create nodes ahead of
time), Rollouts that use `workloadRef` and objects with no name, such as unrendered Helm templates.
`kube-system`, `kube-public` and `kube-node-lease` are skipped unless you pass `--include-system`.

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

SR001, SR004, SR005, SR006 and SR009 apply to long-running workloads (Deployments, StatefulSets,
Argo Rollouts and bare Pods). SR010 to SR013 apply to Jobs and CronJobs. SR002, SR003, SR007 and
SR008 apply to all of them.

The "Spot potential" line is the share of requested CPU in long-running workloads that are ready or
ready with caveats. Jobs and CronJobs are not counted: they request CPU only while they run, so
weighing them like always-on services would overstate their share. The link in the output opens the
[Kubernetes savings calculator](https://www.robertasoliman.com/en/tools/kubernetes-savings-calculator)
with that number filled in as the interruption-tolerant share.

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

- GitHub Action and `kubectl` plugin (krew).
- Later: ScaledJob (KEDA), Argo Rollouts that use `workloadRef`, detecting checkpointing.

## Development

```bash
go test ./...
go run ./cmd/spot-readiness scan testdata
```

Rules live in `internal/rules/rules.go` as small functions and are covered by table tests. A new
rule needs an ID, a severity, a check and a test case.

## License

Apache-2.0. See [LICENSE](LICENSE).
