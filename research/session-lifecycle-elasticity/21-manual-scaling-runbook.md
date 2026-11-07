---
title: 'Manual scaling and riot runs: the commands, as executed'
kind: handoff
state: research
date: 2026-11-06
branch: 'docs-site'
commits: [2aded3a, 25162db, cf5a32e, d37c955]
tags: [session-lifecycle, autoscaling, manual-testing]
---

# Manual scaling and riot runs: the commands, as executed

Everything here was run by hand against the live single-node cluster. None of it
is reproduced by a script in this repository, and none of it is applied by CI.
The point of writing it down is that a reader can tell a hand-made cluster state
from one the repository produces.

| | |
|---|---|
| Status | Manual runbook, current as of 2026-11-06 |
| Date | 2026-11-06 |
| Phase | session lifecycle, GPU cell |
| Branch | `docs-site` |
| Related | [pool split](20-cpu-gpu-pool-split.md), [swap attribution](../swap-capacity/01-attribution-across-classes.md), [swap plan](../swap-capacity/02-swap-attribution-implementation-plan.md) |

## What exists in the cluster

```
sandbox chart            release sandbox, ns agent-sandbox-system, revision 14
  sandboxtemplate        browser-tpl        requests cpu 10m mem 128Mi, limit mem 768Mi
                                            annotation session-lab.io/spill=zram-2to1
  sandboxtemplate        browser-tpl-gpu    same resources, image browser-sandbox-vulkan:local,
                                            privileged, hostPath /dev/dri, same annotation
  sandboxwarmpool        browser-pool       replicas 2, 2 ready
  sandboxwarmpool        browser-pool-gpu   replicas 0

session-lab chart        release session-lab, ns sandbox-demo
  deployment             session-lab-autoscaler
                         --pool=browser-pool --min=2 --max=40 --interval=5s
                         (no --contexts-per-browser, so it assumes 1 session per replica)
```

Two properties of the chart matter for everything below: object names are
hardcoded (`session-lab-autoscaler` and its Role, ServiceAccount, Service and
settings ConfigMap), so the chart installs once and its autoscaler watches one
pool; and the GPU cell ships at `replicas: 0`, so nothing privileged runs unless
someone raises it.

## Scaling: the commands used

All of these are `helm upgrade`, against the release that already exists. They are
manual: nothing in the repository records that they ran, apart from this note.

Raise the GPU cell and hold the CPU cell where it is. `--set-json` replaces the
whole `pools` list, which is the only way to change one entry: `--set` on a list
index replaces the element and drops its other fields.

```bash
# CPU 2 (unchanged) + GPU 4
helm upgrade sandbox config/sandbox -n agent-sandbox-system --reuse-values \
  --set-json 'sandboxes.pools=[
    {"name":"browser-pool","template":"browser-tpl","replicas":2},
    {"name":"browser-pool-gpu","template":"browser-tpl-gpu","replicas":4}]'
```

The first attempt at this failed and the failure is worth keeping:

```
helm history sandbox
  13  failed    conflict occurred while applying object sandbox-demo/browser-pool
                SandboxWarmPool: conflict with "kubectl-patch" ... .spec.replicas
  14  deployed  (rerun with --force-conflicts)
```

A server-side-apply conflict means the field was last written by a `kubectl patch`
rather than by Helm. `--force-conflicts` makes Helm take ownership of it. After
revision 14 the CPU pool stayed at 2 replicas because the command pinned them, and
`browser-tpl`'s CPU request moved 50m → 10m, because the chart's `values.yaml` says
10m and the live template still carried the older value.

Point the existing autoscaler at the GPU pool, with the numbers the CPU pool has
today. This is the zero-file-change way to autoscale the GPU cell, and it costs the
CPU cell its autoscaler: there is one Deployment and one pool flag.

```bash
helm upgrade session-lab config/session-lab -n sandbox-demo \
  --set sandbox.pool=browser-pool-gpu \
  --set autoscaler.minReplicas=2 \
  --set autoscaler.maxReplicas=40 \
  --set autoscaler.interval=5s
```

Autoscaling both cells at once needs a chart change that has not been made: the
object names are literals, so a second install collides on the ServiceAccount,
Role, Service and ConfigMap. The change is to make the name a value and install
the chart twice. Until then: one autoscaler, one pool.

Turn the CPU cell off when only the GPU cell should run:

```bash
helm upgrade sandbox config/sandbox -n agent-sandbox-system --reuse-values \
  --set-json 'sandboxes.pools=[
    {"name":"browser-pool","template":"browser-tpl","replicas":0},
    {"name":"browser-pool-gpu","template":"browser-tpl-gpu","replicas":0}]'
```

## Checking what the cluster thinks

```bash
kubectl get sandboxtemplate,sandboxwarmpool -n sandbox-demo
kubectl -n sandbox-demo get pods -o wide
kubectl -n sandbox-demo get deploy session-lab-autoscaler \
  -o jsonpath='{.spec.template.spec.containers[0].args}'
kubectl -n sandbox-demo get cm session-lab-autoscaler \
  -o jsonpath='{.data.settings\.json}'
kubectl get sandboxtemplate browser-tpl-gpu -n sandbox-demo \
  -o jsonpath='{.spec.podTemplate.metadata.annotations}{"\n"}{.spec.podTemplate.spec.containers[0].resources}{"\n"}'
```

Expected on the GPU template: `{"session-lab.io/spill":"zram-2to1"}` and
`{"limits":{"memory":"768Mi"},"requests":{"cpu":"10m","memory":"128Mi"}}`. The GPU
cell and the CPU cell carry identical resources; only the image and the device
mount differ.

## Riot runs: the commands used

The riot workload is a Go test with a build tag, not a CLI call
(`apps/session-lab/test/riot_live_test.go`). It reads its cell from the
environment since commit `2aded3a`; before that, the pool and template were
hardcoded to the CPU cell.

```bash
cd apps/session-lab

# CPU cell, historical defaults (browser-pool / browser-tpl / normal)
SESSION_LAB_INTEGRATION=1 go test -tags integration -count=1 -timeout 900s \
  ./test/ -run TestRiotLive -v

# GPU cell, same workload
RIOT_POOL=browser-pool-gpu RIOT_TEMPLATE=browser-tpl-gpu \
SESSION_LAB_INTEGRATION=1 go test -tags integration -count=1 -timeout 900s \
  ./test/ -run TestRiotLive -v

# a paced load rather than a correctness pass
RIOT_POOL=browser-pool-gpu RIOT_TEMPLATE=browser-tpl-gpu \
RIOT_SESSIONS=120 RIOT_HOLD=60s RIOT_RATE=2 RIOT_HEAVY=8 RIOT_BOOT=2 \
SESSION_LAB_INTEGRATION=1 go test -tags integration -count=1 -timeout 1800s \
  ./test/ -run TestRiotLive -v
```

The knobs, all read from the environment: `RIOT_SESSIONS` (default 18), `RIOT_HOLD`
(20s), `RIOT_RATE` (2/s), `RIOT_HEAVY` (1 heavy page), `RIOT_BOOT` (0, unbounded),
`RIOT_POOL`, `RIOT_TEMPLATE`, `RIOT_CLASS` (normal), `RIOT_OUT` (report directory,
default `docs/session-lab-runs`). Concurrency is `contexts-per-browser: 8`, fixed
in the test.

The run prints the standard report to stdout and writes `<run>.json` and `<run>.db`
to the output directory. The report carries the per-cell table (`class`, `pool`,
`tier`, sessions, swapped, allowance, swap p95, cpu core-s, queue p95, failures)
because the runner records class and tier per instance.

## Reading a run

```
queue p95 rising with sessions      the platform is queueing, not serving
fail > 0                            a real failure; control-plane refusals are
                                    counted separately and tolerated at 2%
swapped x/N with allowance 0        the tier is declared and the writer never ran
oom kills > 0                       memory limit reached, not swap
sessions with refaults climbing     reclaim pressure, with or without swap
```

Arrival above ~2 sessions/s has been the control plane's limit in earlier runs, and
the claim watch has failed above ~8 concurrent claims. Both show up as refusals
rather than as capacity boundaries, so a run that hits them measures the harness.

## Two knobs to set before a load run

Both exist because a cold Chromium spikes and a full pool hides what happened.

`--boot-concurrency` bounds the startups in flight. Unset, it is unbounded, and
run B shows the cost: 43 instances came up during the run, `boot_cpu_peak_max`
recorded 15.6 "cores" on a 6-core node, and the only measurement of that overlap is
a number that cannot be true. **Use `--boot-concurrency 2`** (or `RIOT_BOOT=2`),
which is what the paced command above does, and the boot spikes stop colliding.

`--health-interval` (default 5s) is how long a browser's probe result is trusted
before `/json/version` is read again on the port the run uses. It is what stops a
dead browser from being handed the queue. Set it to 0 only when the run has no
cluster behind it.

A run that runs out of usable capacity records a `slot_starvation` boundary: every
instance was full or unhealthy and creating another failed. Seeing that in
`first_boundary` is the saturation signal; seeing only `reset_failed` is the bug
this work exists to remove.

## Pod swap: the two pieces, both committed

The node swaps (zram0, 8 GiB). Pods did not, and kubelet is why: its config had no
`memorySwap` block, so it wrote `memory.swap.max = 0` on every container scope.
Both halves are now files:

```
config/k3s/kubelet-swap.conf          KubeletConfiguration drop-in:
                                      failSwapOn: false,
                                      memorySwap.swapBehavior: LimitedSwap
config/k3s/install-k3s.sh:52-58       installs it to
                                      /var/lib/rancher/k3s/agent/etc/kubelet.conf.d/90-swap.conf
scripts/swap-allowance.sh             writes a per-pod allowance, selected by
                                      the pod annotation session-lab.io/spill
```

Applying them needs root, and this session has none (`sudo -n` refuses with "no new
privileges"). The commands, for whoever does have it:

```bash
# 1. kubelet: lets Burstable pods swap at all. Restarting k3s kills every sandbox
#    pod (restartPolicy: Never), so this is a deliberate act.
sudo config/k3s/install-k3s.sh          # installs the drop-in and restarts k3s

# 2. per-pod allowance, on the host, after the restart
sudo scripts/swap-allowance.sh --dry-run
sudo scripts/swap-allowance.sh --verify
sudo scripts/swap-allowance.sh --bytes 67108864      # 64 MiB, the measured peak
sudo scripts/swap-allowance.sh --tier zram-2to1
sudo scripts/swap-allowance.sh --revoke              # back to 0
```

What each mode does: `--dry-run` lists the cgroup files it would write, `--verify`
prints the current value of every one of them, the write mode sets the pod slice
and every container scope (a child cannot exceed its parent, so writing only the
slice leaves kubelet's container-level value in force) and reads each back, failing
if the kernel kept something else. `--revoke` writes 0.

Measured before any of it ran, on a live browser pod in the CPU cell:

```
pod slice                     max
browser container scope       0
sidecar container scope       0
another scope in the pod      max
```

That is why `apps/session-lab` reads the container scopes rather than the slice
(commit `27620c5`): the slice's `max` describes the parent, and the browser cannot
swap.

## The autoscaler was crashlooping: two causes, both fixed

Found on the cluster after the GPU cell was raised by hand:

```
deployment/session-lab-autoscaler args
  --pool=browser-pool-gpu --min=2 --max=80 --interval=5S    <- capital S
pod 0/1 CrashLoopBackOff, 21 restarts
log  flag provided but not defined: -contexts-per-browser
```

Two separate faults. The first was the value: `--interval=5S` is not a duration
Go accepts, so the process exited on flag parsing. The second only showed after
that was corrected: the running image was older than the source, and its binary
has no `--contexts-per-browser` flag at all, so every start died on an undefined
flag while the previous ReplicaSet kept running with the old pool flag.

Fixes, in the order they were needed:

```bash
# 1. a valid duration, against the GPU pool
helm upgrade session-lab config/session-lab -n sandbox-demo \
  --set sandbox.pool=browser-pool-gpu \
  --set autoscaler.minReplicas=2 \
  --set autoscaler.maxReplicas=80 \
  --set autoscaler.interval=5s

# 2. rebuild the image so the binary matches the chart's flags. buildah cannot
#    run in this sandbox (/run/user/1000 is read-only), so the build is Kaniko in
#    the cluster, from git, with the app directory as the context:
kubectl apply -f config/session-lab/build/autoscaler-job.yaml

# 3. re-pull the new image
kubectl -n sandbox-demo rollout restart deploy/session-lab-autoscaler
```

Result:

```
pod session-lab-autoscaler-7787cfbb5f-b7dqc  1/1 Running
log autoscaler: pool sandbox-demo/browser-pool-gpu min=2 max=80 every 5s
log scale up: desired 1 -> 2 (pending 0, ready 0, max 80)
configmap settings: pool browser-pool-gpu, min 2, max 80, interval_s 5, desired 2
warm pools: browser-pool 2/2, browser-pool-gpu 2/2
```

`config/session-lab/build/autoscaler-job.yaml` is committed, so the image can be
rebuilt from the repository. It uses a git context plus
`--context-sub-path=apps/session-lab`; without the sub-path Kaniko looks for
`go.mod` at the repository root and the build fails with
`lstat /kaniko/buildcontext/go.mod: no such file or directory`.

## What this leaves manual

```
the helm upgrades above          not in CI, not in Argo, not in any script
the autoscaler repointing        a live --set, lost at the next upgrade that omits it
the GPU pool replica count       set by whoever runs the experiment
the kaniko build of browser-sandbox-vulkan:local
                                 the Job is committed; the ConfigMap that feeds it
                                 was created by hand:
                                 kubectl -n registry create configmap \
                                   ctx-browser-sandbox-vulkan --from-file=... \
                                   apps/browser-sandbox/{Containerfile.vulkan,entrypoint-vulkan.sh,cdp-bridge.py,cdp-forward.py}
```

The repository reproduces the pod shape and the image recipe. It does not
reproduce the current replica counts, the autoscaler's pool flag, or the build
context ConfigMap.
