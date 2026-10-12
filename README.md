# spot-readiness

Tells you which Kubernetes workloads can run on Spot capacity, and what to fix in the ones that cannot.

"Use Spot for interruption-tolerant workloads" is common advice. Finding out which workloads those
are is usually a guess. `spot-readiness` reads your Deployments, StatefulSets and Pods and checks the
things that decide whether an interruption is a non-event or an outage.

> **Status: v0.1.** It scans YAML/JSON manifests. Reading from a live cluster is planned (see the
> roadmap). It is a static analysis of configuration, not a guarantee: see [Limits](#limits).

## Install

Download a binary for Linux, macOS or Windows (amd64 and arm64) from the
[releases page](https://github.com/robertasolimandonofreo/spot-readiness/releases), and check it
against `checksums.txt`:

```bash
tar -xzf spot-readiness_<version>_linux_amd64.tar.gz
sha256sum -c --ignore-missing checksums.txt
sudo mv spot-readiness /usr/local/bin/
```

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

Example output:

```
VERDICT             SCORE  WORKLOAD                REPLICAS  CPU    SPOT  FINDINGS
not-ready           15     shop/statefulset/db     1         2000m  -     SR001,SR006,SR007,SR009
not-ready           35     shop/deployment/worker  2         2000m  -     SR003,SR004,SR005,SR006,SR009
ready               100    shop/deployment/web     3         1500m  -

3 workloads: 1 ready, 0 with caveats, 2 not ready
Spot potential: 27% of requested CPU (workloads ready or with caveats).
```

`-f`/`--format` accepts `table` (default), `json` and `csv` (`-o`/`--output` are aliases). CSV has one
row per workload with the columns `verdict, score, namespace, kind, name, replicas,
cpu_request_millis, already_on_spot, findings` (rule IDs joined with `;`) and no summary.

`--fail-on` accepts `not-ready` or `caveats`. DaemonSets, Jobs, CronJobs and ReplicaSets are
counted as skipped. `kube-system`, `kube-public` and `kube-node-lease` are skipped unless you pass
`--include-system`.

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

The "Spot potential" line is the share of requested CPU in workloads that are ready or ready with
caveats. The link in the output opens the
[Kubernetes savings calculator](https://www.robertasoliman.com/ferramentas/calculadora-economia-kubernetes)
with that number filled in as the interruption-tolerant share.

## JSON

`-f json` prints a `summary` and a `results` list with each workload's verdict, score and findings.

## Limits

- **Ready does not mean safe.** The tool only sees configuration. It cannot know whether your
  application handles `SIGTERM`, keeps state in memory or tolerates a restart.
- Rules are heuristics. A workload can pass every rule and still be a bad fit for Spot, or fail a
  rule for a good reason. Read the findings, do not just follow the score.
- A PodDisruptionBudget does not stop Spot reclaims. It only limits how many replicas a drain
  removes at once.
- Persistent-volume and local-storage checks look at the pod spec. A `local` PersistentVolume that
  is only visible in the cluster is not detected.

## Roadmap

- v0.2: read workloads from a live cluster (read-only), Markdown output for pull requests.
- v0.3: GitHub Action, `kubectl` plugin (krew).
- Later: Jobs and CronJobs (checkpointing), Argo Rollouts, KEDA.

## Development

```bash
go test ./...
go run ./cmd/spot-readiness scan testdata
```

Rules live in `internal/rules/rules.go` as small functions and are covered by table tests. A new
rule needs an ID, a severity, a check and a test case.

## License

Apache-2.0. See [LICENSE](LICENSE).
