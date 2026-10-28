# E5 — VLM grounding on Chromium screenshots

- **Status:** docs written; no image built, nothing deployed, no measurement taken.
- **Scope:** one new namespace (`vlm-exp`), one new experiment directory
  (`benchmark/experiments/e5-vlm-grounding/`). Nothing under `apps/` or `config/`
  changes. No Argo CD Application, no Helm chart, no NetworkPolicy.
- **Blast radius:** a Kaniko Job in `registry`, a Deployment and a Job in
  `vlm-exp`, one claim from the `browser-pool` warm pool created and deleted by
  the runner. `kubectl delete ns vlm-exp` removes everything except the Kaniko
  Job and its pushed image.

## The question

Three small vision-language models get the same 1280x600 Chromium screenshots and
the same four prompts. What is measured is whether a 0.5B-2B VLM can be an
agent's eyes on a real page: can it name a control, can it put a pixel on it, can
it pick the right one out of look-alikes, and does any of that survive the page
moving under it.

## Hard rule: ONE MODEL AT A TIME

The three models are never loaded, served, or queried concurrently.

- One Deployment replica exists per arm, and only for the arm under test. The
  previous arm is scaled to `replicas: 0` and its Pod is confirmed gone before
  the next arm starts.
- At most one Pod in `vlm-exp` holds `amd.com/render: 1` at any moment. The GPU
  is a single RX 570 (gfx803) reached through Vulkan/RADV, and the host has
  16 GiB of RAM shared with the desktop.
- The runner walks the arms in a loop (`smolvlm2` -> `moondream` -> `qwen3vl`),
  finishing all four task classes for one arm before touching the next.

Reason: this is an experiment about model *quality* on identical inputs, not
about co-tenancy. Two arms resident at once would change prefill latency,
risk Vulkan OOM, and make the numbers incomparable.

## The four tasks

Every task is asked of every arm, against the same saved frame.

| # | Task | Answer shape | Scored as |
|---|---|---|---|
| 1 | Identify a named control ("which element is the refresh button") | free text label | label match after normalisation |
| 2 | Return that control's approximate screen coordinates | `{x, y}` in 1280x600 viewport pixels | hit inside 32 px of the labelled centre; raw distance recorded; 64 px band reported separately |
| 3 | Find the target among several similar elements | element label + `{x, y}` | correct element AND a coordinate hit |
| 4 | Repeat after the page changes: after `scroll dy=+600`, and after navigating to a second page | same shape as 2 and 3 | same scoring, reported per state |

Task 4 exists because a screenshot-only model that is right on a static page and
wrong after a scroll is not usable by an agent. The two states (scrolled,
navigated) are separate rows so a failure cannot hide in an average.

## The three arms

All three run on `llama-server` with the Vulkan backend, one model plus its
mmproj projector, `temperature: 0`.

| Arm | Weights | Vision projector | Notable server args |
|---|---|---|---|
| `smolvlm2` | `ggml-org/SmolVLM2-500M-Video-Instruct-GGUF`, `SmolVLM2-500M-Video-Instruct-Q8_0.gguf` | `mmproj-SmolVLM2-500M-Video-Instruct-Q8_0.gguf` | `-ngl 99 -c 4096 --threads 4` |
| `moondream` | `ggml-org/moondream2-20250414-GGUF`, `moondream2-text-model-f16_ct-vicuna.gguf` | `moondream2-mmproj-f16-20250414.gguf` | `-ngl 99 -c 4096 --threads 4` |
| `qwen3vl` | `ggml-org/Qwen3-VL-2B-Instruct-GGUF`, `Qwen3-VL-2B-Instruct-Q4_K_M.gguf` | `mmproj-Qwen3-VL-2B-Instruct-Q8_0.gguf` | `-ngl 99 -c 4096 --threads 4 --jinja` |

`--jinja` is required for Qwen3-VL's chat template; the same prompt text is used
for all three arms so the comparison is not shifted by prompt wording.

Weights are downloaded by an init container onto the `vlm-models` PVC in
`vlm-exp` and reused across runs. The image carries no weights.

## Inputs: the sandbox console, read-only

The console already exposes what the experiment needs; nothing is added to it.

| Need | Call | Reference |
|---|---|---|
| list claimed sandboxes | `GET /api/sandboxes` | `apps/sandbox-console/server.py:962` |
| one frame, fresh | `GET /sandboxes/{id}/shot?fresh=1` -> 1280x600 PNG | `server.py:534`, viewport in `config.py:74` |
| scroll | `POST /api/sandboxes/{id}/scroll {"dy": 600}` | `server.py:1006` |
| navigate | `POST /api/sandboxes/{id}/url {"url": "..."}` | `server.py:987` |
| click (for label verification only) | `POST /api/sandboxes/{id}/click {"x":..,"y":..}` | `server.py:997` |
| claim a sandbox | `POST /sandboxes {"kind":"claim"}` | `server.py:684` |
| delete it | `DELETE /sandboxes/{id}` | `server.py:724` |

The runner reaches the console at
`http://sandbox-console.sandbox-console.svc.cluster.local:8080`. The console
namespace has no NetworkPolicy, so no rule has to be added for this.

The sandbox reaches `192.168.0.250` (the console's LoadBalancer) by itself: the
template-derived sandboxes allow egress `0.0.0.0/0` and block `10/8`. The
ClusterIP form is unreachable from inside the sandbox, so the pages the fixture
navigates to are addressed by LB IP.

## Fixtures and labels

- `fixtures/tasks.json` — the prompt set: four task classes x two page states,
  fixed text, fixed target names.
- `fixtures/labels.json` — ground truth: the target's viewport pixel centre per
  task, annotated by hand from the captured PNGs, plus `tolerance_px: 32`.
- `results/frames/` — the exact PNG bytes each arm was shown, so a label can be
  audited against the frame it refers to.

Labels are hand-annotated, which is the main caveat in the write-up. A 32 px hit
radius on a 1280x600 frame is generous for a control-sized target and strict for
"somewhere on the page"; both the hit rate and the raw distance distribution are
reported.

## Build and run

```bash
# image: llama.cpp + Vulkan + libmtmd vision, in-cluster, no host root
kubectl apply -f benchmark/experiments/e5-vlm-grounding/manifests/kaniko-vlm-server.yaml
kubectl -n registry wait --for=condition=complete job/kaniko-vlm-server --timeout=900s
kubectl -n registry logs job/kaniko-vlm-server | grep -i pushed

# namespace + weights PVC
kubectl apply -f benchmark/experiments/e5-vlm-grounding/manifests/namespace.yaml

# one arm, then the next, never together
kubectl apply -f benchmark/experiments/e5-vlm-grounding/manifests/vlm-arm.yaml   # ARM=smolvlm2
kubectl -n vlm-exp rollout status deploy/vlm-smolvlm2
kubectl apply -f benchmark/experiments/e5-vlm-grounding/manifests/runner-job.yaml # MODEL_URL=http://vlm-smolvlm2:8000
```

## Teardown

```bash
kubectl -n registry delete job kaniko-vlm-server
kubectl delete ns vlm-exp
```

The pushed image `192.168.0.28:5000/vlm-server:local` and its Kaniko cache repo
stay in the registry, and are the one thing teardown does not remove.

## Related

- `benchmark/experiments/e3-amd-gpu/` — proved Vulkan/RADV works on this card and
  that the grant (`amd.com/render: 1`) is what makes `/dev/dri` usable.
- `benchmark/experiments/e4-gpu-e2e/` — the in-cluster Job + ConfigMap runner
  shape this experiment reuses.
- `research/features/sandbox-agent_experiment.md` — the console route table with
  measured costs (0.58 s for a fresh 82,662 B frame).
