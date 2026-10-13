## Spot migration plan

| | |
|---|---|
| Total workloads | 3 |
| Ready now | 1 |
| Ready after changes | 1 |
| Manual review | 1 |
| Keep on on-demand | 0 |
| Requested CPU | 5.5 vCPU |
| Immediately movable to Spot | 1.5 vCPU (27%) |
| Potential after changes | 3.5 vCPU (64%) |

### Phase 1: move now

- `shop/deployment/web` (1.5 vCPU, confidence high)

### Phase 2: move after these changes

| # | Workload | CPU | Changes | Score | Confidence |
|---|---|---|---|---|---|
| 1 | `shop/deployment/worker` | 2 vCPU | SR003, SR004, SR005, SR006 | 35 | high |

### Manual review

- `shop/statefulset/db` (2 vCPU, confidence low): Persistent state detected: uses persistent volumes: zonal disks make rescheduling slower and tied to one zone

Before deciding, look at:

- storage topology (zonal disks, storage class, volume binding)
- replication and quorum: how many copies, and whether the rest keeps serving when one is lost
- recovery time: how long a replacement pod takes to be useful again
- availability zone dependency: whether the volume pins the pod to one zone

_The plan reads configuration only: test each move and keep on-demand capacity as a fallback._
