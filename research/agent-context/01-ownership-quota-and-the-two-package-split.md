---
title: '01 — Ownership, quota and the two-package split'
kind: design
date: 2026-11-08
branch: docs-site
tags: [agent, identity, quota, isolation, design]
---

# 01 — Ownership, quota and the two-package split

| | |
|---|---|
| Status | Design written, nothing built |
| Date | 2026-11-08 |
| Epic | `experiments-lnu` |
| Fits | `apps/agent-runtime/` (new), `benchmark/experiments/e7-agent-context/` |
| Capacity | 350 slots live `[O]`; one RX 570 for inference `[O]` |
| Labels | `[O]` operator-stated, `[I]` arithmetic over a stated figure, `[U]` unmeasured |

## 1. Decisions

| # | Decision | Value | Status |
|---|---|---|---|
| D1 | Who owns the browser | agents acquire ephemeral sessions from the shared pool; a user-dedicated browser is one E7 arm, not the default | proposed |
| D2 | Reasoning cadence | one plan of up to 5 operations per inference; re-plan on failure or page change | proposed |
| D3 | Page representation | accessibility tree plus versioned element refs; a PNG is one measured arm | from E5/E6 |
| D4 | Picker | Laya answers a choice question over the candidate refs it is handed; Qwen plans when no candidate set resolves the target | measured shape of both models |
| D5 | Permission enforcement | evaluated in code before dispatch, never by asking the model | adopted |
| D6 | Persistence | `persistent: false` in every E7 arm; no resume path exists in this app | scope |
| D7 | Session identity | the session request carries `TTL` and `dedicated` and nothing else | house rule |
| D8 | Agent and AgentRun | `Agent` is a definition (model, tool set, grant); `AgentRun` is one execution with its own budget | adopted |
| D9 | Cost currency | inference wall-seconds and session-seconds; this host exposes no per-process AMD accounting, so "GPU-seconds" is not reported | measured constraint |
| D10 | Package boundary | `internal/user` and `internal/agent` never import each other; `cmd/` imports both | enforced by test |

D1 and D2 are the operator's call and are written as proposals. The alternative to each is measured
as a control arm in E7, so either answer changes a default rather than the design.

## 2. Topology

```
   caller ──POST /agents──► cmd/agent-runtime (HTTP)
                                │
                     internal/user   ── identity, quota, permission scope
                                │         (knows no browser, no agent)
                                ▼
                          Grant{Actions, Domains, MaxOpsPerCall}
                                │
                     internal/agent  ── AgentRun: objective, budget, lifecycle
                                │         (knows no Chromium, no session)
                    ┌───────────┴────────────┐
                    ▼                        ▼
              Reasoner                   Tool
           (Qwen / Laya)          (inspect_page, act)
                                             │
                                   internal/agent/check.go
                                   refs + version + grant, in code
                                             │
                                             ▼
                                apps/sandbox-console :8080
                                (existing; unmodified by this study)
                                             │
                                             ▼
                                     Chromium session
                                (apps/session-lab owns capacity)
```

The console is reached over its own HTTP API, which already exposes
`GET /sandboxes/{id}/shot`, `POST /api/sandboxes/{id}/click`, `.../scroll`, `.../url` and a
`/cdp` passthrough that accepts any DevTools method — the same route E6's controller used for
`Accessibility.getFullAXTree` and `DOM.getBoxModel`.

## 3. The two packages

| Package | Owns | Exported surface | Forbidden knowledge |
|---|---|---|---|
| `internal/user` | identity, quota, permission scope | `User`, `Limits`, `Grant`, `Registry` | sessions, browsers, models, objectives, tools |
| `internal/agent` | objective, reasoner, tools, run lifecycle, budget | `AgentInput`, `Decision`, `Op`, `Ref`, `Snapshot`, `Reasoner`, `Tool`, `AgentRunner` | users, quotas, sessions, Chromium, HTTP |

`internal/user` hands out a `Grant` of **plain strings** — `Actions []string`,
`Domains []string`, `MaxOpsPerCall int` — which is the property that lets it stay ignorant of
`internal/agent`. `internal/agent` reads the grant through its own type, so it never names `user`.

Enforcement lives in `cmd/agent-runtime/boundary_test.go` and shells out to `go list -deps`:

```
./internal/user  must not reach  internal/agent
./internal/agent must not reach  internal/user
```

Composition is the only place both appear. A reviewer can delete `cmd/` and both packages still
compile, which is the test of whether the split is real.

## 4. Interfaces

```go
type Reasoner interface {
    Decide(ctx context.Context, input AgentInput) (Decision, error)
}

type Tool interface {
    Name() string
    Execute(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

type AgentRunner interface {
    Run(ctx context.Context, task Task) (Result, error)
}
```

Around those three, the input and the decision carry the two ideas worth adopting from the browser
agent literature:

```go
type AgentInput struct {
    Objective   string
    Step        int
    Snapshot    Snapshot        // the only page view a reasoner receives
    Candidates  []Node          // non-empty => pick, do not plan
    LastActions []ActionResult
    Budget      Budget
}

type Decision struct {
    Kind DecisionKind           // Plan | Done | Fail
    Ops  []Op                   // 0..grant.MaxOpsPerCall, executed as one batch
    Why  string
}

type Op struct {
    Action string               // click | type | scroll | navigate
    Ref    Ref
    Text   string
}
```

`Decision.Ops` being a slice is the bulk-action idea: the number of browser operations does not have
to equal the number of inferences. `Ref` carries a snapshot version, which is the staleness fix.
Both are infrastructure, so both are testable with no model and no browser.

## 5. Snapshots and versioned refs

```
snapshot 13:  2. button "Sign in"     5. link "Forgot"     9. textbox "Email"
              ref 13:2                ref 13:5              ref 13:9
```

A `Tool` call that inspects the page increments the version. An `Op` names a ref as
`(version, id)`. The gate accepts the op only when `op.Ref.Version == snapshot.Version` and the id
exists in that snapshot and the node is enabled.

Two failure modes this removes, both observed in E6:

- a reply that points at a node that no longer exists (E6 logged nine of these as
  `browser.action.rejected`);
- a batch whose second op lands on a page the model did not see, because the first op navigated.

## 6. The gate

`Check(policy, snapshot, ops) (allowed []Op, rejects []Reject)` in
`internal/agent/check.go` runs before
dispatch and is the only authority on whether an op happens.

| Check | Reject code |
|---|---|
| action in `grant.Actions` | `action-not-granted` |
| `op.Ref.Version == snapshot.Version` | `stale-ref` |
| ref id present in the snapshot | `unknown-ref` |
| node not disabled and has a box | `not-actionable` |
| `navigate` host in `grant.Domains` | `domain-not-granted` |
| `len(ops) <= grant.MaxOpsPerCall` | `batch-too-large` |

A reject on any op stops the batch at that op: earlier ops keep their result, later ops never
dispatch. Reject codes reuse E6's event vocabulary so the two experiments' logs stay comparable.

## 7. The run loop and the budget

```
inspect_page()  ─► Snapshot v            tool, deterministic
      │
   Decide(input) ─► Decision             reasoner, may be zero inferences
      │
   gate.Check(grant, v, ops)             in code
      │
   execute ops in order                  stop the batch on page change
      │
   observe: url, snapshot version, frame hash
      │
   Done / re-plan
```

`Budget{MaxInferences, MaxOps, Deadline}` is held by the runner, not the reasoner. A run that
exhausts either cap ends as `Result{BudgetExhausted: true}`, which is a scored outcome.

## 8. The API

| Endpoint | Body | Returns |
|---|---|---|
| `POST /agents` | `{objective, agent_id}` | `{run_id, state}` |
| `GET /agents/{id}` | — | `{run_id, state, step, inferences, ops, result}` |
| `POST /agents/{id}/cancel` | — | `{state}` |

`agent_id` names a definition (reasoner, tool set, grant) created from configuration; the run
resolves it, then asks the session layer for capacity. No user id travels to the session layer, and
no objective travels to it either.

## 9. Cost arithmetic

The workload figure is 300,000 agent tasks a day `[O]`, i.e. 3.47 tasks/s `[I]`.

Session-seconds: one task holds a session for its duration. At 350 slots `[O]`:

| Task holds a session | Sustained tasks/s | Tasks/day `[I]` |
|---|---|---|
| 30 s | 11.7 | 1,008,000 |
| 120 s | 2.9 | 252,000 |
| 300 s | 1.2 | 100,800 |

Inference-seconds, one lane, per task:

| Inference latency | Inferences/task | Tasks/s `[I]` | Tasks/day `[I]` |
|---|---|---|---|
| 0.7 s `[O]` E5 median reply | 1 | 1.43 | 123,400 |
| 0.7 s | 2 | 0.71 | 61,700 |
| 0.7 s | 10 | 0.14 | 12,300 |
| 3.0 s `[U]` longer plans | 2 | 0.17 | 14,400 |
| 3.0 s `[U]` | 10 | 0.03 | 2,880 |

The 0.7 s figure is the median Qwen3-VL reply measured in E5 for a short JSON answer; a plan of
several operations is longer and 3.0 s is an assumption, not a measurement. Either way the
inference column is the binding one by an order of magnitude, and it moves by a factor of five with
the cadence. That is the number E7 exists to measure rather than assume.

## 10. What this note does not build

No resume, no durable state, no scheduler changes, no model training, no multi-tenant gateway
beyond an in-memory quota registry, and no change to `apps/session-lab/` or `apps/sandbox-console/`.
