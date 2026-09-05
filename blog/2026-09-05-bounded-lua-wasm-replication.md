---
title: "A bounded Lua/Wasm replicator for k3d"
date: 2026-09-05
tags:
  - k3d
  - kubernetes
  - cilium
  - webassembly
  - lua
  - wasmtime
  - runwasi
  - golang
  - vector
  - elixir
  - distributed-systems
  - mlops
---

# A bounded Lua/Wasm replicator for k3d

This is a deliberately small experiment: Lua running in a WebAssembly runtime behaves like a worm. Each instance emits a numeric sample and asks for another instance. It may reproduce only until a fixed memory budget is consumed.

The point is not autonomous, unbounded propagation. The point is a visible, bounded replication protocol that Kubernetes can restart, constrain, observe, and stop.

## Ownership

Each component has exactly one job.

| Component | Job | Must not own |
|---|---|---|
| Lua/Wasm worker | Emit `{instance_id, sequence, x, y}` and a replication intent | Kubernetes credentials, scaling, regression state |
| Go replicator controller | Accept replication intents, enforce the cap, and reconcile the worker Deployment replica count | Event transformation or regression |
| Vector | Receive, validate, enrich, batch, and route sample events | Scaling decisions or ML calculation |
| Elixir application | Receive samples and maintain the linear-regression result | Replication policy or Kubernetes writes |
| Kubernetes | Schedule pods, restart failed containers, and enforce resource limits | Application-level reproduction policy |

The worker is self-replicating by intent: it asks to reproduce. The Go controller is the only actor that can turn that request into a new pod. This keeps the experiment bounded and prevents every worker from needing RBAC permission to create or scale workloads.

## Execution and observability decision

One worm is one Kubernetes Pod. The worker Pod uses a Wasm runtime class backed by the pinned `runwasi` Wasmtime shim rather than a conventional Linux application container. This removes the need for a general-purpose guest userspace while retaining Kubernetes scheduling, cgroup limits, restart behavior, and a separate Cilium endpoint for every worm.

The alternative is a Lua executor Pod hosting many logical Lua VMs. That model has higher runtime density, but all logical worms share one network namespace, IP address, Cilium identity, and NetworkPolicy. Hubble would show only the executor-to-service flow, not one endpoint per worm. It is intentionally rejected for this experiment because Cilium visibility is more important than maximum logical-instance density.

The worker Deployment therefore scales real Pods. Every worker has its own `app=lua-worker` label and a unique pod identity, so Hubble can show its flow to Vector and to the replicator. We accept Kubernetes-per-Pod overhead in exchange for that direct topology view.

## Minimal topology

```text
                         control plane
Lua/Wasm worker ──POST /replicate──> Go replicator ──patch scale──> Deployment
      │                                                               │
      └───────────────────────────────────────────────────────────────┘

                           data plane
Lua/Wasm worker ──POST /events──> Vector ──POST /ingest──> Elixir regression
```

The Go controller owns a `Worm` custom resource, for example:

```yaml
apiVersion: lab.example.io/v1alpha1
kind: Worm
metadata:
  name: sample-generator
spec:
  maxReplicas: 20
  workerMemoryLimit: 32Mi
status:
  desiredReplicas: 7
  acceptedReplications: 6
```

The controller accepts an intent only while `status.desiredReplicas` is below `spec.maxReplicas`. Its reconciliation loop patches the owned worker Deployment to that desired count. An increment must be serialized by the API server using resource version conflict retries, so two simultaneous worker requests cannot silently overwrite each other.

Initial budget: `20 replicas × 32 MiB = 640 MiB`. Apply `limits.memory: 32Mi` to every worker, set `maxReplicas: 20`, and place the experiment in a namespace with a `ResourceQuota` limiting worker memory to 640 MiB. The quota is the backstop if controller code is wrong; the CRD limit is the intended policy.

## Cilium topology now

The current k3d cluster has one k3s server and no agents, so it has one node failure domain. Cilium can define and observe the logical network topology, but it cannot make this setup tolerant of node loss. A single node dying takes the entire experiment with it.

Start with labels and CiliumNetworkPolicies, not a service mesh:

```text
app=lua-worker        -> app=vector        TCP 8080, HTTP POST /events
app=lua-worker        -> app=replicator    TCP 8081, HTTP POST /replicate
app=vector            -> app=regression    TCP 4000, HTTP POST /ingest
app=replicator        -> kube-apiserver    TCP 6443 only
```

Use namespace-scoped `CiliumNetworkPolicy` resources with a default-deny posture for these pods. The worker receives no ServiceAccount token and no Kubernetes RBAC. The Go controller receives only the permissions it needs: read/watch the `Worm` resource and get/patch the scale subresource of its managed Deployment. Vector and Elixir receive no Kubernetes write permissions.

Hubble provides the first useful topology view: it shows actual worker-to-Vector, worker-to-controller, and Vector-to-Elixir flows. This is more useful than a diagram because it reports the connections that really occurred.

## Topology later

When the experiment moves beyond one k3d node, add physical placement only where it changes a failure outcome:

1. Run at least three schedulable nodes before claiming node-failure tolerance.
2. Apply `topologySpreadConstraints` for Vector and Elixir across `kubernetes.io/hostname`; a PodDisruptionBudget then protects voluntary eviction, not node failure.
3. Run two Vector aggregators and two Elixir replicas only after deciding how regression state is shared or reconstructed. Two independent in-memory regression windows do not create one highly available result.
4. If remote or multi-cluster workers are added, use Cilium ClusterMesh only when cross-cluster service discovery and policy are actually needed. It is not part of the first experiment.

The progression is therefore: first prove bounded reproduction and event flow on one node; then prove Cilium policy; finally add placement and replicated state when there are multiple failure domains to benefit from them.

## Why Vector is the stream receiver

Vector is the data-plane entry point rather than the controller because it is already suited to fan-in, validation, batching, retry, buffering, and future fan-out. Workers send simple HTTP events to Vector. Vector can apply VRL to reject malformed samples, attach arrival timestamps and Kubernetes metadata, and forward normalized events to the Elixir ingestion endpoint.

Replication intents remain off that route. They are control-plane messages and go directly to the Go controller. Keeping data and control flows separate makes it obvious which event caused a new worker and prevents an ETL rule from accidentally becoming scaling policy.

## First build boundary

Build only these four deployables: worker, Go controller, Vector, and Elixir. Use a single Vector and a single Elixir replica in the one-node cluster. The controller cap, per-pod memory limit, and namespace quota are mandatory before the worker can request replicas.
