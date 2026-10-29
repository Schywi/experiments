# session-lab

The pool autoscaler: it keeps `SandboxWarmPool` sized to the sessions that are waiting for an
instance.

## Why this exists

The cluster runs no HorizontalPodAutoscaler and no KEDA (`kubectl get hpa -A` is empty, and the
`keda.sh` CRDs are absent). The upstream examples scale the warm pool on GKE Managed Prometheus,
which this cluster does not run. This is the smallest substitute that produces real elasticity:

- **Demand signal:** claims in the sandbox namespace that are not Ready yet. A claim the platform
  gave up on (Ready=False with `PodFailed`, `Failed`, `TemplateNotFound`, `WarmPoolNotFound`) is
  counted separately and is not demand.
- **Control:** the pool's `spec.replicas`, patched only when the computed target differs.
- **Target:** `ready claims + pending claims`, clamped to the configured `minReplicas` and
  `maxReplicas`. Those bounds are configuration; the platform's observed capacity is whatever an
  experiment measures, and the two are reported apart.
- **Scale-out latency:** measured from the decision until the pool is as large as asked and the queue
  is empty, exported as `session_lab_autoscaler_scale_out_seconds`.

## What it publishes

Its settings and a heartbeat land in the ConfigMap `session-lab-autoscaler` in the sandbox
namespace. `apps/session-lab` reads that ConfigMap at preflight and records it with the run, so a
report states what was scaling the pool instead of assuming it:

```
autoscaling       hpa=0 keda=false session-lab-autoscaler min=2 max=40 every 5s (heartbeat 2s ago)
```

A heartbeat older than 30 seconds is reported as `STALE`.

## Install

```bash
# image, from apps/session-lab/Containerfile
buildah build --network host -t session-lab-autoscaler:local apps/session-lab
config/registry/push-image.sh session-lab-autoscaler

helm upgrade --install session-lab config/session-lab \
  --namespace sandbox-demo --set autoscaler.maxReplicas=40
```

Stop it before a run whose fixed pool you want to measure:

```bash
helm uninstall session-lab --namespace sandbox-demo
```

## RBAC

`get/list/watch` on `sandboxclaims`, `get/list/watch/patch/update` on `sandboxwarmpools`, and
`get/create/update` on `configmaps` — all in one namespace, nothing cluster-scoped.

## Limits

One replica, on purpose: two autoscalers patching one pool would fight. It does not read CPU or
memory of the pods, so it cannot be fooled by an idle browser looking busy; it also cannot see a
pool that is full of pods nobody wants, which is why scale-down follows the claim count and not the
pod count.
