# Feature (experiment): an agent that drives a browser sandbox

> Status: **proposed, nothing implemented.** No line of this exists in code yet.
> The sandbox console is not modified until we decide to build it. This note
> records the design we agreed on, in the terms we agreed on, so the next
> conversation starts from the same place.

## What the experiment is for

One running Chromium sandbox, two agents that want it:

```
agent1 takes sandbox2, plays for 2 min, releases
agent2 takes sandbox2, plays for 30 s, releases
```

The question is not "who is allowed". The question is: **once an agent has
finished, is the sandbox really free, does the next agent start clean, and how
long can a session last before the sandbox or the pool gives out.**

Agents have IDs. A scheduler hands out sandboxes. That is the whole access
model. No tokens, no auth, no leases with epochs. This runs on a lab LAN.

## What already works today

No new code. Measured against `browser-pool-22grt` (RFC §4):

| The agent wants to | Route | Cost |
|---|---|---|
| see the page | `GET /sandboxes/{id}/shot?fresh=1` → 1280x600 PNG | 0.58 s, 82,662 B |
| click something | `POST /api/sandboxes/{id}/click {x,y}` | 0.81 s |
| scroll | `POST /api/sandboxes/{id}/scroll {dy}` | 0.15 s |
| go to a URL | `POST /api/sandboxes/{id}/url {url}` | 1.62 s |
| know where it is | tile `url` (read from `location.href`) | free |
| get a sandbox | `POST /sandboxes {kind:"claim"}` | ~1.5 s from the warm pool |
| throw it away | `DELETE /sandboxes/{id}` | — |

So: **can an agent interact with a Chromium page? Yes.** Click and scroll are
real input events, not DOM injection.

## The lock

One dict in the console process. The console runs one uvicorn worker at
`replicas: 1`, so memory is the right place for it.

```python
LOCKED = {}   # sandbox_id -> {"agent": "a1", "until": 1792..., "taken": 1792...}
```

| Route | Body | Answer |
|---|---|---|
| `POST /api/sandboxes/{id}/take` | `{"agent":"a1","ttl_s":120}` | `200 {agent, until}` or `409 {locked_by:"a2", until}` |
| `POST /api/sandboxes/{id}/release` | `{"agent":"a1"}` | `200 {released:true}` or `409 {not_yours:"a2"}` |
| `GET /api/sandboxes/{id}/lock` | — | `{agent, until}` or `{agent:null}` |

Rules:

- take on a free or expired entry wins;
- take on a live entry held by someone else is `409`;
- release by a non-holder is `409`;
- any `click`/`scroll`/`url` carrying `X-Agent: a2` against a sandbox locked to
  `a1` is `409`;
- past `until`, the entry is dead: the next action from the holder answers
  `410 {expired:true}` and the entry is cleared.

Release does two more lines, because otherwise agent 2 inherits agent 1:

```python
SCREEN._shots.pop(sandbox_id, None)      # the cached frame, 10 s TTL (server.py:318)
await cdp.navigate(sandbox_id, "about:blank")
```

Agent 2 then navigates to whatever it wants. If a caller wants a factory-fresh
browser instead, `DELETE` + `POST /sandboxes` already does it in ~1.5 s.

## The agent

Two of them, sharing four actions, because they answer two different questions.

### Agent A — scripted, no model

A loop that clicks, scrolls and re-reads the page for N seconds. Deterministic.

Used for the reliability question: session duration, lock handoff, whether the
frame cache leaks across holders, whether a 2-minute session survives. A model
in the loop would add noise to every number and would get blamed for failures
that belong to the sandbox.

### Agent B — model-driven

Same four actions, chosen by the cluster's own LLM.

Observation handed to the model is **text**, not the PNG:

```
url: http://192.168.0.250:8080/
title: sandbox console
[0] a "sandboxes" (12, 84)
[1] button "claim" (640, 300)
[2] input "url" (400, 120)
```

The model answers one action per turn:

```json
{"action": "click", "i": 1, "why": "claim adopts a warm sandbox"}
{"action": "scroll", "dy": 400, "why": "rest of the page"}
{"action": "goto", "url": "http://192.168.0.245/", "why": "asked for grafana"}
{"action": "done", "summary": "the claim button is at 640,300"}
```

It names an **element index**, never a coordinate. The harness maps the index to
the rect it measured, so a model that cannot count pixels can still click the
right button.

Why text and not vision: the only LLM on this cluster is
`Qwen2.5-1.5B-Instruct-Q4_K_M` (`apps/models/llm/chart/values.yaml`), text-only,
CPU-only. A vision model is a new deployment, a new set of weights, and on this
hardware a 3B VL model at a few tokens per second. A 1.5B text model reading a
list of labelled elements is the cheap path, and it needs one route that does
not exist yet (below).

## What is missing, and it is small

| Gap | Why it blocks | Route |
|---|---|---|
| **typing** | search boxes and forms are unreachable | `POST /api/sandboxes/{id}/type {text}` → `Input.insertText` |
| **keys** | Enter and Tab after typing | `POST /api/sandboxes/{id}/key {key}` → `Input.dispatchKeyEvent` |
| **a text view of the page** | Agent B needs labels and rects, not a PNG | `POST /api/sandboxes/{id}/elements` → one `Runtime.evaluate` returning `{tag, text, role, x, y, w, h}` per interactive element |

All three are pass-throughs: `cdp-bridge.py` already accepts any CDP method, and
`Runtime.evaluate` / `Input.insertText` / `Input.dispatchKeyEvent` are named in
RFC §11. `/elements` returns a fixed shape rather than arbitrary JavaScript, so
it cannot become a general escape hatch.

That is the entire addition to the console: three routes, and the lock. Nothing
else in the existing system is touched.

## Capacity, with the numbers we have

| Number | Value | Source |
|---|---|---|
| warm pool, unclaimed | 8 | `sandboxwarmpool/browser-pool` |
| sandboxes alive now | 10 (8 pool + 2 claims) | `kubectl -n sandbox-demo get sandboxes` |
| actively-driven Chromium this host sustains | ~9.1 | `config/sandbox/README.md`, experiment 02 |
| cold start | ~40 s | template comment |
| warm adoption | ~1.5 s | measured |

So this host supports **one or two concurrent live sessions** while keeping the
pool intact, and each extra agent costs one pool member. `MAX_CONCURRENT_SANDBOXES=9`
is a refusal threshold, not a capacity.

## How to verify (once built)

```bash
id=browser-pool-22grt
curl -sS -X POST -H 'Content-Type: application/json' -d '{"agent":"a1","ttl_s":120}' \
     http://192.168.0.250:8080/api/sandboxes/$id/take
curl -sS -X POST -H 'Content-Type: application/json' -d '{"agent":"a2","ttl_s":30}' \
     http://192.168.0.250:8080/api/sandboxes/$id/take          # expect 409
curl -sS -X POST -H 'Content-Type: application/json' -d '{"agent":"a1"}' \
     http://192.168.0.250:8080/api/sandboxes/$id/release
curl -sS -X POST -H 'Content-Type: application/json' -d '{"agent":"a2","ttl_s":30}' \
     http://192.168.0.250:8080/api/sandboxes/$id/take          # expect 200
```

Contamination check for the handoff: agent 1 sets `localStorage.seen=1`, releases,
agent 2 reads it back. Run it once with the `about:blank` reset and once without.

## Non-goals

No authentication. No lease tokens or fencing epochs. No expiry reaper process.
No screencast. No Playwright in the loop. No new dependency: `httpx` is already
in the repo. The audit log stays one JSONL file with a new `agent` field.

## What is still open

1. Does the sandbox get recycled (`DELETE` + claim) or reset in place
   (`about:blank` + clear storage) at handoff? Measurement, not argument.
2. Session length: 120 s, 300 s, or whatever the failure data says.
3. Whether Agent B is worth its cost, given a 1.5B CPU model at the other end.
   Agent A alone answers every reliability question in the title.
