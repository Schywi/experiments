# RFC — Canary and traffic splitting for agent-sandbox (experimental)

| | |
|---|---|
| Status | Draft for review — **experimental, not required by the console** |
| Date | 2026-10-24 |
| Scope | Investigating whether Cilium's L4/L7 primitives can serve as a canary mechanism for agent-sandbox workloads |
| Split out of | `rfc-sandbox-console.md` §11, where the first version of this analysis was wrong |
| Cluster | single-node k3s `v1.36.5`, Cilium **1.20.1**, `enable-l7-proxy=true`, `enable-envoy-config=true` |

---

## 1. Summary

Cilium can split traffic. Two of the three mechanisms are already available on this cluster. The
open question is not *"can Cilium split"* but *"is network splitting the right way to canary a
browser sandbox"*, and the answer is mostly **no** — with one exception that is worth building.

| Mechanism | Layer | Available here | Use for agent-sandbox |
|---|---|---|---|
| `cilium service update --weights` | L4, per-Endpoint | **yes, live today** | only for the router or a gateway Service |
| `CiliumEnvoyConfig` | L7, arbitrary Envoy | **yes, CRD present** | header-based routing between router versions |
| Gateway API `HTTPRoute` weighted `backendRefs` | L7 | **no** — CRDs and `GatewayClass` absent | the same thing, with a supported API, if installed |
| Allocation split at claim time | not network at all | yes, plain Kubernetes | **the actual browser canary** |

Recommendation: **E2 (allocation split) first**, E1 (Cilium L4 weights) second as a capability test,
E3 (L7 header split) last and probably never.

## 2. Why "traffic split" is three different things here

This is where the first draft of the console RFC went wrong. The word covers three unrelated
mechanisms, and only one of them fits a browser sandbox.

```
                     ┌──────────────────────────────────────────┐
  layer 1  L4        │ Service sandbox-router                   │  interchangeable
                     │   backend A 90%   backend B 10%          │  replicas → weights WORK
                     └──────────────────────────────────────────┘
                     ┌──────────────────────────────────────────┐
  layer 2  L7        │ CiliumEnvoyConfig / HTTPRoute            │  route by header/path
                     │   X-Sandbox-ID: sb-* → router-v2         │  → WORKS, but what splits?
                     └──────────────────────────────────────────┘
                     ┌──────────────────────────────────────────┐
  layer 3  alloc     │ SandboxClaim → which SandboxTemplate?    │  not the network at all
                     │   90% browser-v1 template, 10% v2        │  → THIS is the browser canary
                     └──────────────────────────────────────────┘
```

The distinction that matters: **L4 weights distribute requests across endpoints that are supposed
to be identical.** A sandbox is not a replica. Each one is a distinct identity, addressed by its
own `X-Sandbox-ID`, holding its own browser state. There is no pool of interchangeable sandboxes to
weight — sending 10% of a given sandbox's requests to a different sandbox is meaningless, and
sending them to a different *version* of the same sandbox would break its session.

So layer 1 and layer 2 can only canary **the shared component in front of the sandboxes** — the
router, or a browser gateway if you build one. Layer 3 is what canaries **the browser itself**, and
it is a scheduling decision, not a network one.

## 3. What this cluster actually has

Verified on 2026-10-24, so the RFC does not rely on upstream docs:

| Capability | Primitive | Present here | Evidence |
|---|---|---|---|
| L4 weighted backends | `cilium service update --id N --weights a,b` | **yes** | Cilium 1.20.1 daemon; agent is reachable with `kubectl -n kube-system exec ds/cilium -- cilium …` |
| L7 Envoy injection | `CiliumEnvoyConfig`, `CiliumClusterwideEnvoyConfig` | **yes** | CRDs `ciliumenvoyconfigs.cilium.io`, `ciliumclusterwideenvoyconfigs.cilium.io` created 2026-10-06 |
| L7 proxy enabled | `enable-l7-proxy` | **yes** | `cilium-config` → `enable-l7-proxy=true` |
| Envoy config enabled | `enable-envoy-config` | **yes** | `cilium-config` → `enable-envoy-config=true` |
| Gateway API | `GatewayClass`, `HTTPRoute` | **no** | `kubectl get gatewayclass` → *the server doesn't have a resource type*; no `gateway.networking.k8s.io` CRDs |
| Ingress | Cilium shared ingress controller | yes | `cilium-ingress` at `192.168.0.240`, `loadbalancerMode: shared` |
| LB-IPAM | `CiliumLoadBalancerIPPool` | yes | `config/k3s/cilium-lb-ipam.yaml`, `192.168.0.240–250`, **no weights** |
| L2 announcements | `CiliumL2AnnouncementPolicy` | yes | same file; one active announcer, **no weights** |
| Mutual auth (SPIFFE) | `authentication` in `CiliumNetworkPolicy` | probably — not exercised here | Cilium 1.20 supports it; verify maturity before relying on it |
| Progressive delivery | Argo Rollouts / Flagger | **no** | Argo **CD** is installed (`.247`), Rollouts is not |
| Flow observation | Hubble | yes | `.241` — observability only, mutates nothing |

Two consequences worth stating plainly:

- The **supported, declarative** way to do weighted L7 splitting on Cilium is Gateway API, and it is
  not installed. Adding it means installing the Gateway API CRDs plus a Cilium `GatewayClass` — a
  cluster-level change, not an app change.
- `CiliumEnvoyConfig` can do the same thing today with no new CRDs, but you are hand-writing Envoy
  config. Sharp tool, no schema validation of your intent.

## 4. Experiments

Each is small, has a hypothesis, and says what would refute it.

### E1 — Can Cilium weight L4 backends on a Service? (capability test)

**Hypothesis:** two replicas of the router behind one Service can be driven to a 90/10 split with
`cilium service update --weights`, and Hubble shows the distribution.

```
# find the service id and backend ids
kubectl -n kube-system exec ds/cilium -- cilium service list | grep sandbox-router
kubectl -n kube-system exec ds/cilium -- cilium service update --id <N> --weights 9,1 --frontend <ip:port>
```

**Measure:** 1,000 requests through the Service, count backends by log line or by Hubble flow.
**Refute if:** the distribution does not track the weights, or the weights do not survive the next
Endpoints churn / Cilium restart.
**Why it matters:** it is the only L4 split primitive available today, and it is imperative — it
lives outside GitOps. If it does not survive reconciliation, it is a demo trick, not a mechanism.

**Expectation:** this splits traffic to *the router*, not to sandboxes. Value is canarying the
router binary itself, which is a small component you rarely change. Low product value, cheap to
test.

### E2 — Canary the browser image by allocation split (the one worth building)

**Hypothesis:** a browser canary does not need traffic splitting. Two `SandboxTemplate`s, two
`SandboxWarmPool`s, and an allocation rule at claim time gives a clean A/B with no network
component.

```
browser-v1 template → pool browser-v1 (replicas 18)
browser-v2 template → pool browser-v2 (replicas 2)     ← 10%
claim arrives → console/pool selector picks a pool by percentage
```

**Measure per cohort**, over the same page set:

| Metric | Where it comes from |
|---|---|
| `t_ready` p50/p95 | §6.5 timers in `rfc-sandbox-console.md`, or `agent_sandbox_claim_startup_latency_ms` |
| `t_cdp` p50/p95 | timer; this is Chromium boot, the number a new browser build most affects |
| page-load success rate | screenshot non-blank + `Runtime.evaluate("document.title")` non-empty |
| memory working set per sandbox | metrics-server or VictoriaMetrics |
| OOM / crash count | pod restart count |
| render fidelity | pixel diff between cohorts on a fixed page |

**Acceptance:** both cohorts reach Ready, the split holds within ±2% over 100 claims, and the
metrics table has a row per cohort. **Refute if:** a v2 sandbox cannot be told apart from a v1 by any
of those metrics — then the canary needs a different signal (LLM task success), and allocation
splitting alone is not enough.

**Why this is the right shape:** it canaries the thing you actually change (the Chromium/Playwright
image), it needs no new cluster capability, and it is pure Kubernetes — no Envoy, no imperative
`cilium` calls, survives GitOps.

### E3 — L7 header split between router versions (only if E1 disappoints)

**Hypothesis:** `CiliumEnvoyConfig` can route `X-Sandbox-ID` traffic to two router versions
without touching the Service.

Sketch, not a manifest: a `CiliumEnvoyConfig` selecting the router pods, two Envoy clusters, a
`RouteConfiguration` whose virtual host is chosen by a header match with a `weighted_clusters`
fallback for the un-matched case.

**Refute if:** the config needs a Cilium restart to apply, or breaks the router's websocket upgrade.
**Priority:** lowest. The router is small, rarely changes, and E1 already covers versioning it.

## 5. What a browser canary should measure

Worth writing down because "traffic split" implies a metric, and browser workloads make the
obvious metrics misleading:

- **Do not use HTTP 5xx rate.** A failed browser session usually returns 200 with a blank page.
- **Page-load success must be semantic**: title non-empty, body text over a floor, screenshot not
  the blank-page colour. This is what `t_paint` in the console RFC already computes.
- **Report `t_cdp` separately from `t_ready`.** `t_ready` is Kubernetes being fast; `t_cdp` is the
  browser being fast. A canary cares about the second.
- **Integer restarts beat averages.** One OOM in 2 sandboxes is invisible in a mean.

## 6. Prerequisites and cost

| Need | Cost |
|---|---|
| E2 | nothing new. Two templates, two pools, a selector function |
| E1 | nothing new. `kubectl exec` into the Cilium agent |
| E3 | nothing new, but hand-written Envoy config and a maintenance burden |
| Gateway API route (variant of E3) | install CRDs + a Cilium `GatewayClass`; cluster-level, git-committed, reviewable |
| Real progressive delivery | Argo Rollouts or Flagger, not installed |

Memory note: two cohorts means two pools. At 250 MiB per warm sandbox, every pool costs its replica
count in idle RAM. A 10% canary of a 20-replica pool is 2 extra warm browsers = 500 MiB held for the
duration of the experiment. That is the honest price and it is small.

## 7. Non-goals

- Split per sandbox. Not meaningful: sandboxes are not interchangeable.
- Replace whatever the product uses for feature flags. Allocation split is a rollout mechanism, not
  an experimentation platform.
- Canary the *cluster* (Cilium, k3s). Out of scope; this is about the sandbox payload.
- Add Gateway API to this cluster in this RFC. It is listed as an option, not a task.

## 8. Risks

| Risk | Mitigation |
|---|---|
| Imperative `cilium service update` drifts from GitOps and vanishes on restart | treat E1 as a capability probe, not a mechanism; expect it to be refuted as durable |
| Hand-written Envoy config breaks silently | E3 is last and optional; if it needs this much care, skip it |
| Two pools double the idle RAM and halve the demo's headroom | cap the canary pool at 2–3 replicas; run on a quiet node |
| Canary metrics look fine while task success drops | add a semantic check (§5), not just latency |
| Single-node cluster: any Cilium restart is an outage during the experiment | run experiments when nothing else is being demoed |

## 9. Recommendation

1. **Build E2.** It is the browser canary, it is plain Kubernetes, and it is ~100 lines on top of
   the console's existing create-a-claim path. One config value: `canary_percent`.
2. **Run E1 once** and record the result. It costs an hour and settles whether Cilium's imperative
   weights are durable. Expect "no".
3. **Skip E3** unless E1 refutes and a router canary becomes a real need.

Nothing here is required for the demo. This RFC exists so the console's §11 decision is based on
what the cluster can actually do, instead of a claim that the primitive does not exist.
