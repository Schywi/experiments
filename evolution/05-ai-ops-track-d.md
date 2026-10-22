# Phase 5 — AI operations assistant, Track D (2026-10-06 → 2026-10-12)

**Branch:** M1–M3 on `trackc` (`aaf6f81`, `8060efd`, `ddb9519`), M5–M10 on
`native-k3s-gpu-models` (`74af8bf` → `3321626`), pipeline `db846fa`, provenance
`70ee10b`, topology `8be6c07`, tag **`e2-laya-vs-qwen-e2e`** at `5586cc0` ·
**tracked paths:** [`../apps/assistant/`](../apps/assistant/README.md),
[`../research/features/`](../research/features/README.md),
[`../benchmark/`](../benchmark/README.md)

## The question

Once the cluster had models (Phase 3) and a GPU (Phase 4), the interesting
question was not "can it answer questions". It was: **can an LLM answer
infrastructure questions in a way an operator can check instead of trust?**

That framing drove every design decision below.

## The shape of an answer

```text
question ──► known intent? ──yes──► deterministic bundle (0 LLM calls)
                   │no
                   ▼
                 PLAN (1 LLM call) ──► FAN-OUT (threadpool, args validated)
                                             ├─ k8s API   ├─ PromQL
                                             ├─ Neo4j     └─ docs corpus
                                             ▼
                                    facts, capped per-fact and in total
                                             ▼
                                 CORRELATE (1 LLM call) ──► {screen_md, speech}
                                             ▼
                          provenance attached server-side: at · source · reproduce · links
```

- **Two LLM calls, not one per tool** (`db846fa`). A deterministic bundle covers
  17 of 27 benchmark intents at zero inference cost; the rest take one plan call
  and one correlate call that emits the screen text and the spoken line together.
- **The model never sees the provenance** (`70ee10b`). `at`, `source`,
  `reproduce` and `links` are attached *after* the tools return: zero tokens,
  and the model cannot fabricate a link.
- **Fail closed.** Malformed output is never returned as the answer: the
  `{screen_md, speech}` shape is validated, retried once, and degrades to a
  digest of the evidence actually gathered (`fccf051`, plus the pytest contract
  from `bd experiments-qib`).
- **Read-only by construction.** The ServiceAccount has `get/list/watch` on a
  fixed resource set and `get` on `pods/log`. The only mutating tool,
  `propose_action`, mints a single-use 120-second confirmation token;
  execution needs a human `POST /actions/confirm` **and** `rbac.allowActions`
  (`9f12797`).
- **Bounded concurrency.** An in-process limit with a bounded FIFO queue in
  front of the LLM (`4300050`), so a burst of chat turns cannot stampede a
  1.5B model running on six CPU threads.

## Milestones, as they were actually built

| Milestone | What it added | Commit |
|---|---|---|
| M1 | text chat loop over the local LLM | `aaf6f81` |
| M2 | read-only `get_pods` + RBAC + audit | `8060efd` |
| M3 | `get_pod_logs`, `get_events` | `ddb9519` |
| M5 | observability tools (PromQL, Grafana/Hubble links) | `74af8bf` |
| M5–M7 | knowledge corpus, investigation flow | `ee52e12` |
| M8 | decoupled voice (TTS cache, background synthesis) | `2b9e126` |
| M9 | controlled actions behind confirmation | `9f12797` |
| M10 | memory (notes + session history) | `3321626` |
| M11+ | `get_topology` (live API) + `get_topology_snapshot` (Cartography/Neo4j) | `8be6c07` |

The M1–M3 prototype was written on `trackc`; M5 onward superseded it on
`native-k3s-gpu-models`. That is why the unification merge (`9b449fe`) resolved
every `apps/assistant/*` conflict in `native-k3s-gpu-models`' favour.

## Benchmarks, because "it feels fast" is not a result

- **E2** — Laya vs Qwen as the planner, end-to-end, isolated deployment
  (`0f921e1`, tag `e2-laya-vs-qwen-e2e`): the reason Laya is now scaled to 0.
- **E3** — CPU vs GPU Qwen, Phase 4.
- **E4** — an A/B assistant variant is in progress in the working tree and is
  **not yet tracked or registered in Argo CD**; `ROADMAP.md` §4 tracks that.

Baselines, scripts and findings: [`../benchmark/`](../benchmark/README.md) and
[`../benchmark/correlate-findings.md`](../benchmark/correlate-findings.md).

## The exposure posture this phase produced

The assistant made the cluster worth showing, which forced the security
questions Phase 1 had deferred:

- **Bare Cilium LB IPs instead of DNS tricks** (`b945699`) — `nip.io` and
  `home.arpa`/`/etc/hosts` approaches were tried and dropped as fragile.
- **Argo CD anonymous read-only, pinned** (`b6a6581`) — `users.anonymous.enabled`
  with `policy.default: role:readonly`, at `192.168.0.247`. `server.disable.auth`
  is forbidden in `AGENTS.md` because it grants the Admin role to anyone.
- **Permissive CORS** (`e415607`) so the external `fuwa` UI can call the API —
  documented as a known exposure (`49a6436`, `bd experiments-yc4`), not silently
  accepted.

Grafana still runs without login and Neo4j still runs `NEO4J_AUTH=none`. Those
are deliberate LAN-lab trade-offs, listed as limits in
[`../ROADMAP.md`](../ROADMAP.md) §5, and they are exactly what the unbuilt
public/VPS profile would have to fix first.
