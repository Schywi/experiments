# RFC — Environments, immutable artifacts, and promotion

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-24 |
| Scope | Why "the current assistant" keeps getting overwritten, and the smallest fix that makes promotion possible |
| Evidence base | `ROADMAP.md` §4 open work items 1–2, `experiments/README.md` §GitOps, `config/registry/`, `config/argocd/applications/`, `apps/assistant/chart/values.yaml` |
| Related | `rfc-sandbox-console.md`, `rfc-traffic-split.md`, `research/platform-cockpit-deep-research-prompt.md` |

---

## 1. Summary

You could not promote between environments because **there was no version to promote**. The
`assistant` chart ships `image.tag: local` against a mutable in-cluster registry tag, so every
`buildah push` overwrites what is running and Argo CD sees no diff. Environments are the second
problem; a mutable tag is the first, and it blocks everything downstream — including Kargo, which
promotes *artifacts* and cannot promote something that has no identity.

Fix order: **immutable tags → per-environment overlays → ephemeral preview stage → Kargo.**
Kargo is right, but it is step four, and on this node it must manage a disposable stage rather than
three resident environments.

## 2. What actually happened — evidence

Four separate defects, all verifiable in the tree today.

| # | Defect | Evidence |
|---|---|---|
| D1 | **The image tag never changes.** `tag: local` on a mutable `:local` push target, with `pullPolicy: Always` | `apps/assistant/chart/values.yaml` — `repository: 127.0.0.1:5000/assistant`, `tag: local`, comment: *"pushes a mutable `:local` tag, so a cached image must not be reused"* |
| D2 | **Argo tracks a branch, not a version.** `targetRevision: native-k3s-gpu-models` | `config/argocd/applications/assistant.yaml:19` — every sync takes the branch tip |
| D3 | **Variants live as parallel Applications in one namespace**, standing in for environments | `config/argocd/applications/{assistant,assistant-laya}.yaml`, both `namespace: assistant`; plus a hand-applied `assistant-e4-gpu` |
| D4 | **One deployment was applied by hand** | `experiments/README.md` §What runs: *"`fuwa` and the E4 assistant variant were applied by hand; they are not Argo Applications yet"* |

D1 is the root. With a mutable tag:

- **No rollback target exists.** "Roll back to the previous image" is impossible — the previous
  bytes were overwritten in place. A tag that moves is not a version.
- **Argo CD reports `Synced` while behaviour changes.** The chart and the branch are unchanged, so
  there is no diff to show. The cluster drifts silently on the next pod restart, which makes the
  Argo UI actively misleading.
- **Nothing can be promoted.** Kargo, Flagger, a `kubectl set image`, a git tag — all of them need
  an immutable reference to point at.
- **`pullPolicy: Always` is load-bearing and invisible.** It exists to paper over the mutable tag.
  Under an immutable tag it becomes a needless registry round-trip on every restart.

D2 compounds it: even with immutable tags, tracking a branch means the "version" of a deployment is
whatever someone pushed five minutes ago. A branch is a moving pointer; an environment needs a fixed
one.

D3 and D4 are why there was nothing to promote *to*. One namespace, three Deployments, one live
surface.

## 3. Root cause, in one sentence

The pipeline had three stages — write code, build, run — with the middle stage having no output
identity, so "running" was always the same moving thing.

```
 today                                    what promotion needs
 buildah bud → push :local → Argo sync    build → 1.9.3 / sha-4f2a → pin in staging → verify → pin in prod
              ↑                            ↑
              mutable, no identity          immutable, addressable, rollback-able
```

## 4. The ladder

Four rungs. Each is useful alone; each is a prerequisite for the next.

### L0 — Immutable artifacts *(the enabling change)*

- Tag every image with the **git commit SHA**, and optionally a semver. Never `local`, never
  `latest`.
- Reference by **digest** in the environment that matters: `@sha256:…`.
- Drop `pullPolicy: Always → IfNotPresent` once tags are immutable.
- Keep `:local` as a developer-only tag that no manifest references.

Blocker to name: the registry is `127.0.0.1:5000`, a **loopback** registry with `hostNetwork: true`
(`config/registry/README.md`). It is reachable from the node and from pods on that node, and from
nowhere else. That is fine for one node and it is fine for Kargo, **as long as Kargo's image
discovery reads tags from that same endpoint**. Confirm this before designing around Kargo; if it
cannot, L0 needs a registry with a stable address first.

### L1 — Per-environment overlays

Kustomize overlays over the existing Helm chart, or `values-<env>.yaml`:

```
apps/assistant/chart/                  (unchanged, still Helm)
config/envs/
  staging/values.yaml    image.digest: sha256:…   replicas: 1
  prod/values.yaml       image.digest: sha256:…   replicas: 1
config/argocd/applications/
  assistant-staging.yaml   targetRevision: cluster-evolution   path: .../staging
  assistant-prod.yaml      targetRevision: v1.9.3 (a TAG, not a branch)   path: .../prod
```

Two rules that must hold:

- **One reconciler per resource.** `ROADMAP.md` §5 already records what happens otherwise (the
  pinned `stable/` bundle and `applications/vector.yaml` describe the same Vector resources and may
  not be registered together). Each environment owns its own namespace; no Application may select
  another's objects.
- **`targetRevision` for prod is a tag or a full commit**, never a branch. This is the same
  contextual-branch rule from `AGENTS.md`, applied to environments: a branch is a moving pointer,
  and only the moving environments may track one.

### L2 — Ephemeral preview stage *(the answer to "I only have one node")*

Three resident environments do not fit. The box is 6 vCPU / 15.5 GiB with memory **limits already
at 89%** and requests at 37%; adding two more Assistants plus their scrapes is not available.

So the middle stage is disposable rather than resident:

```
PR / commit
   → build sha image (L0)
   → create namespace preview-<sha> from the staging overlay
   → run the app's own tests against it
   → keep it for N minutes, then delete
   → promote = write the digest into config/envs/prod/values.yaml, one commit
```

Test gates that already exist and cost nothing:

| Gate | Command | Where it lives |
|---|---|---|
| Unit | `cd apps/assistant && python3 -m pytest -q` | `experiments/AGENTS.md` §Testing |
| Manifest validity | `bash config/ci/validate.sh` | Helm + kubeconform over `config/` and `apps/` |
| Planner A/B | E2 benchmark, tag `e2-laya-vs-qwen-e2e` | `benchmark/` |

The preview stage is a Kargo `Stage` with an ephemeral verification step. You can have it today as a
script plus a namespace TTL.

### L3 — Kargo

Only after L0–L2 exist.

| Kargo concept | Maps to |
|---|---|
| `Warehouse` | watches `127.0.0.1:5000/assistant` for new tags — **requires L0**, and requires that discovery works against a loopback registry |
| `Freight` | one immutable artifact set (image digest + chart revision) |
| `Stage` | `staging`, `prod`, and `preview` (ephemeral) |
| `Promotion` | moves Freight stage→stage; each step can be a `git` commit, an `argocd-update`, a `helm` upgrade, or a verification Job |

What it buys you over L1+L2 by hand: promotion becomes a reviewed object with a history, instead of
a commit someone remembers to make. What it costs: an API server, controller, and webhooks in a
`kargo` namespace, plus a new failure mode on the cluster that serves the demo.

**Recommendation: do not install Kargo until L2 has been run by hand three times.** If the manual
loop is still awkward after three runs, Kargo will fix it. If it is smooth, Kargo is a dashboard for
a process you already have.

## 5. Target layout

```
                    ┌──────────────┐
  git push ────────►│ build sha im │──► 127.0.0.1:5000/assistant:sha-4f2a
                    └──────────────┘    (+ @sha256:… digest)
                             │
        ┌────────────────────┼────────────────────┐
        ▼                    ▼                    ▼
  preview-4f2a          assistant-staging     assistant-prod
  (ephemeral, TTL 60m)  (branch: cluster-     (tag: v1.9.3)
   tests + E2 gate       evolution, digest)    digest pinned
        │                    │                    │
        └──── promote: write digest ─────────────►┘
```

Namespaces: `assistant` (prod), `assistant-staging`, `preview-<sha>`. Variants like
`assistant-laya` stop being Applications; they become benchmark arms run in a preview namespace and
torn down.

## 6. Milestones

| # | Milestone | Acceptance test |
|---|---|---|
| M1 | Immutable tags | two successive builds produce two different tags; `kubectl get deploy assistant -n assistant -o jsonpath='{.spec.template.spec.containers[0].image}'` returns a digest or SHA tag, never `:local` |
| M2 | Rollback works | pin an older digest, Argo syncs, `/chat` responds; re-pin forward. Both directions observed |
| M3 | Staging overlay exists | `assistant-staging` Application is `Synced/Healthy` in its own namespace; prod is untouched by a staging sync |
| M4 | Preview lifecycle | one command creates `preview-<sha>`, runs pytest + `validate.sh`, and deletes the namespace; the pod count returns to baseline |
| M5 | Promotion is one commit | moving the digest from staging to prod is a single commit; `git log` shows who promoted what and when |
| M6 | Kargo decision | L2 has been run by hand 3×; a written decision (adopt / defer) with the three runs as evidence |

## 7. Non-goals

| Rejected | Why |
|---|---|
| Auth on any of this | the demo runs faster without it; LAN posture, stated in `experiments/README.md` §Limits |
| Multi-cluster promotion | one node |
| HA control plane | one node |
| Three resident environments | does not fit in 15.5 GiB with limits at 89% |
| Canary percentages between environments | that is `rfc-traffic-split.md`; different problem, different layer |
| Replacing Argo CD | Kargo orchestrates Argo, it does not replace it |

## 8. Risks

| Risk | Mitigation |
|---|---|
| Mutable tag returns by habit (`:local` is convenient) | make `:local` unreferenceable: no manifest may use it; a `validate.sh` check greps for `tag: local` |
| Loopback registry turns out invisible to Kargo's warehouse | verify discovery *before* designing for Kargo; otherwise a stable-address registry is L0's real first task |
| Two Applications reconcile the same object | one namespace per environment; `ROADMAP.md` §5 already documents the failure |
| Promotion becomes a manual `kubectl set image` again | prod overlay + digest in git; nothing else writes to prod |
| Preview namespaces leak and consume the pod cap (110) | TTL is mandatory, and the creator deletes on failure too |
| The assistant is a single uvicorn worker with a bounded queue (`apps/assistant/concurrency.py`) | staging must not be load-tested at prod rates; it will 429 by design, which is correct behaviour, not a regression |

## 9. Open questions

1. **Semver or SHA only?** SHA alone is sufficient for promotion and needs no discipline. Semver adds
   a human name and a decision ("is this 1.9.3?"). Recommend SHA for the pipeline, semver tag added
   only on a prod promotion.
2. **Does `assistant-laya` survive?** As an Application it is a variant pretending to be an
   environment. Recommend: delete the Application, keep the chart, run it as a benchmark arm in a
   preview namespace.
3. **Kargo vs a 40-line script?** The script wins on a single node until there is more than one
   application to promote. That threshold is the decision point, not the feature list.
4. **What gates prod?** Today: `pytest` and `validate.sh`. The E2 planner benchmark is the honest
   gate for a model-backed service, and it needs a fixed baseline commit to compare against —
   `benchmark/` already records one per run.
