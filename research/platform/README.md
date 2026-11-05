---
title: 'Platform'
kind: index
date: 2026-11-04
tags: [control-plane]
---

# Platform

What the cluster runs on, and what would have to change to run it somewhere else.

| Note | Kind | Subject |
|---|---|---|
| [Native k3s + GPU + CPU models](native-k3s-gpu-models-plan.md) | plan | The pivot off `k3d` onto the Fedora host, and the decisions recorded with it |
| [Production hosting on GKE or GCE](production-hosting-gke-gce.md) | analysis | What cannot work there, and what must change first |

For how the platform behaves under load, read
[`../session-lifecycle-elasticity/`](../session-lifecycle-elasticity/README.md),
[`../cpu-oversubscription/`](../cpu-oversubscription/README.md) and
[`../swap-capacity/`](../swap-capacity/README.md).
