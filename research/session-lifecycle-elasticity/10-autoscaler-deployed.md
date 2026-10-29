# Note 10 — The autoscaler, deployed and observed

| | |
|---|---|
| Status | Recorded, deployment + live verification |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Chart | `config/session-lab/` |
| Image | `127.0.0.1:5000/session-lab-autoscaler:local`, built in-cluster by `config/registry/kaniko-build.sh` |
| Runs | verification run `20261029T171417Z` |
| Related | [08 — requirement status](08-requirement-status.md), [09 — three runs](09-results-three-runs.md) |

---

## 1. What was missing

The autoscaler existed as a binary that I started from a shell for one experiment. That is a
demonstration, not a deployment: nothing kept it running, nothing gave it credentials, and the run
that recorded its settings read them from a ConfigMap a shell process happened to be writing.

## 2. What is deployed now

| Object | Where | What it is |
|---|---|---|
| `Deployment/session-lab-autoscaler` | `sandbox-demo` | one replica, non-root, read-only root filesystem, 20m/32MiB requested |
| `ServiceAccount` + `Role` + `RoleBinding` | `sandbox-demo` | `get/list/watch` on `sandboxclaims`, `get/list/watch/patch/update` on `sandboxwarmpools`, `get/create/update` on `configmaps`, nothing cluster-scoped |
| `Service/session-lab-autoscaler` | `sandbox-demo` | ClusterIP on `:9110` for `/metrics` |
| `ConfigMap/session-lab-autoscaler` | `sandbox-demo` | the live settings and heartbeat a run reads |
| VictoriaMetrics job | `config/victoriametrics` | pod discovery by label `app.kubernetes.io/name=session-lab-autoscaler`, replacing the static host target |

The image is built by Kaniko from the branch, because buildah cannot run in this environment
(`/run/user/1000/libpod` is read-only). The chart's README records the build and install commands.

## 3. Live verification

Started from `spec.replicas: 0`, autoscaler at `min=2 max=40 interval=5s`. A run of 20 sessions at
0.5 arrivals/s with 30 s holds, offering concurrency 15:

| Time | Pool desired | Pool ready | Autoscaler log |
|---|---|---|---|
| 17:13:48 | 2 | 2 | `scale up: desired 0 -> 2 (pending 0, ready 2)` |
| 17:14:23 | 4 | 4 | `scale up: desired 2 -> 4` |
| 17:14:28 | 7 | 7 | `scale up: desired 4 -> 7` |
| 17:14:33 | 9 | 9 | `scale up: desired 7 -> 9` |
| 17:14:38 | 12 | 12 | `scale up: desired 9 -> 12` |
| 17:14:43 | 15 | 3 | `scale up: desired 12 -> 15` — ready lagging, cold starts |
| 17:16:18 | 22 | 22 | `scale-out complete: 22 ready after 84.9s` |
| 17:17:38 | 2 | 2 | `scale down: desired 22 -> 2 (pending 0, ready 2)` |

Scale-out and scale-down both happened without a hand on the replica count. Scale-out latency from
the first decision to a fully ready pool was **84.9 s** for 22 replicas — that is the number a
waiting session actually pays, and it is exported as
`session_lab_autoscaler_scale_out_seconds`.

## 4. The control plane was the casualty, again

During the run the k3s API server stopped answering for about 45 seconds. Both clients recorded it:

- runner: `dial tcp 127.0.0.1:6443: connect: connection refused`, four sessions ended
  `acquisition_failed`;
- autoscaler: `dial tcp 10.43.0.1:443: connect: connection refused`, then `apiserver not ready`,
  and one `publish settings: unexpected EOF`.

The node stayed `Ready`, `kubectl get --raw=/readyz` answered `ok` afterwards, and k3s was still
active — memory at the time was 10 of 15 GiB used with 3 GiB of swap. This is the same boundary the
host-run autoscaler met in note 09, now reproduced from inside the cluster. The pool reached 22
replicas and node memory fell to 34.1% available before the outage.

**Consequence that had to be fixed:** the run's cleanup failed during the outage, so sixteen claims
survived and the autoscaler — correctly reading them as demand — held the pool at 22 with no traffic
at all. `release()` and the `destroy` path now retry the delete five times with backoff; a leftover
claim is not a harmless loose end when something is scaling on the claim count.

## 5. Requirement status after this

| # | Requirement | Status |
|---|---|---|
| 3 | Real elasticity | **verified end to end**: implemented, deployed as a workload, configured `min=2 max=40`, observed scaling 2 → 22 and back to 2, scale-out 84.9 s |
| 6 | Metrics and dashboards | runner and autoscaler series both scrape in-cluster; 12 dashboard panels; **Grafana still at 0/0**, so the dashboard has not been seen with data |
| 5 | Overload / scale-out / delay / recovery | verified — and the recovery path found a defect (cleanup under an API outage) that is now fixed |

## 6. What this changes about the platform

The autoscaler now scales the pool whether or not an experiment is running, which means the pool no
longer reflects what an operator set — it reflects demand. Two consequences:

1. A run that wants a **fixed** pool must stop the autoscaler first
   (`helm uninstall session-lab -n sandbox-demo`), or its `--pool-replicas` will be overwritten
   within five seconds. This is the conflict the audit asked about under "two reconcilers".
2. The `sandbox-demo` pool sits at the configured minimum between experiments, so the node is not
   idle-cost-free: two Chromium pods stay warm by design.

## 7. How to check this note

```bash
helm -n sandbox-demo status session-lab
kubectl -n sandbox-demo get deploy,svc,sa,role,rolebinding -l app.kubernetes.io/name=session-lab-autoscaler
kubectl -n sandbox-demo logs deploy/session-lab-autoscaler --tail=30
kubectl -n sandbox-demo get configmap session-lab-autoscaler -o jsonpath='{.data.settings\.json}'
kubectl -n sandbox-demo get sandboxwarmpool browser-pool -o jsonpath='{.spec.replicas}{"\n"}'
```
