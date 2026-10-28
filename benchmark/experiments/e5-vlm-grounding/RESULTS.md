# E5 results — three VLMs on real Chromium screenshots

**Verdict: only Qwen3-VL-2B produced a usable answer, and only on one of seven
tasks. SmolVLM2-500M and Moondream 1.9B scored 0/7. Qwen3-VL was also 3-8x
faster per task and the only arm that answered in the required JSON shape every
time.**

Measured 2026-10-28 in `vlm-exp` on the single Fedora node (6 vCPU, 16 GiB, AMD
RX 570 via Vulkan), one arm resident at a time. Frames, prompts, labels and
scoring were identical across arms; only the weights changed.

## Scoreboard

| Arm | Valid JSON | Task fully correct | Label right | Best coordinate error | Median latency |
|---|---|---|---|---|---|
| `qwen3vl` — Qwen3-VL-2B-Instruct Q8_0 | **7/7** | **1/7** | 1/7 | 52.0 px (task 2) | **702 ms** |
| `moondream` — Moondream 1.9B f16 | 1/7 | 0/7 | 0/7 | 112.4 px (task 2) | 2,273 ms |
| `smolvlm2` — SmolVLM2-500M Q8_0 | 0/7 | 0/7 | 0/7 | — (no coordinates emitted) | 3,101 ms |

Hit radius is 32 px; 52 px counts as a miss and falls inside the 64 px band that
is reported separately.

## Per task

| Task | Frame | `qwen3vl` | `moondream` | `smolvlm2` |
|---|---|---|---|---|
| 1. Name the "+ claim" button | home | **hit** (`+ claim`, 124 ms) | miss (prose) | miss (prose) |
| 2. Coordinates of "+ claim" | home | miss, 52.0 px from (58,97) | miss, 112.4 px | miss (prose) |
| 3. Delete button of card `browser-pool-65l7z` among 4 look-alike cards | home | miss (label quoted the question; x sent as `[414, 924]`) | miss (prose) | miss (prose) |
| 4. Probe button of card `browser-pool-sthfr` | scrolled 420 px | miss (x as `[100, 155]`) | miss (prose) | miss (prose) |
| 5. Delete button of card `browser-pool-ww79w` | scrolled 420 px | miss (x as `[414, 599]`) | miss (prose) | miss (prose) |
| 6. Down-arrow button in the open dialog | deep link | miss (x as `[845, 74]`) | miss (prose) | miss (prose) |
| 7. Which sandbox owns the open dialog | deep link | miss (answered `sandbox-console`) | miss (prose) | miss (prose) |

## What each failure actually was

- **SmolVLM2-500M: no instruction following.** Every reply was a paragraph of
  prose that restated the page ("The image shows a screenshot of a sandbox
  console interface…") and hit the 320-token limit mid-sentence. It never emitted
  a JSON object, at `max_tokens` 160 or 320, so it produced no coordinate to
  score. It did say the right words about the page ("+ claim", "browser-pool-65l7z"),
  which is what a captioning model does; it did not answer the question.
- **Moondream 1.9B: one valid answer, wrong place.** On task 2 it returned
  `{"label": "+ claim", "x": 38, "y": 145}` — the label correct, the point 112 px
  from the true button at (58, 97). The other six replies were prose. It is the
  fastest at nothing: median 2,273 ms with a 6,416 ms outlier.
- **Qwen3-VL-2B: format compliance, weak localisation.** It answered JSON on all
  seven tasks, immediately and with no rambling. Task 1 was correct. Task 2 was
  the interesting one: correct label, coordinates (38,145) for a button at
  (58,97) — right region, 52 px off. Tasks 3-6 it understood the question well
  enough to repeat the target description and emit *candidate lists* instead of
  a point (`"x": [414, 924], "y": 600`), which is a format failure the scorer
  counts as invalid, not a wrong guess. Task 7 it answered "sandbox-console"
  rather than the sandbox id.

## Latency and cost

| Arm | Wall per task (min / median / max) | Prompt tokens | Completion tokens | Weights on the PVC |
|---|---|---|---|---|
| `qwen3vl` | 124 / 702 / 900 ms | 831-888 | ≤ 50 | 2.1 GB + 0.9 GB mmproj |
| `moondream` | 713 / 2,273 / 6,416 ms | 97-160 | ≤ 320 | 3.5 GB + 0.7 GB mmproj |
| `smolvlm2` | 921 / 3,101 / 3,267 ms | 336-395 | 320 (truncated) | 0.5 GB + 0.2 GB mmproj |

Qwen3-VL's prompt tokens are 2-8x the others because its template expands the
image into far more tokens; it still finishes 3x faster because it stops after
a short answer instead of generating 320 tokens of prose.

## Method, and what it does not cover

- Ground truth is each element's `getBoundingClientRect()` centre, read from the
  page's own DOM through the console's CDP bridge in the same second the PNG was
  captured, and re-verified afterwards with `document.elementFromPoint` at every
  labelled point (`ALL_POINTS_RESOLVE`). No hand-eyeballed coordinates.
- Three frames, 1280x600, devicePixelRatio 1, so CSS pixels and image pixels are
  the same number. Every arm was shown byte-identical PNGs from the `vlm-frames`
  PVC.
- Seven tasks is a smoke test of capability, not a benchmark. One hit is not a
  ranking.
- Sending a *list* of candidate points is counted as a miss. A harness that
  accepted `x: [414, 924]` would score Qwen3-VL higher; this one demands the
  format the agent loop needs.
- Moondream needed a different transport: its GGUF chat template has no image
  slot, so `/v1/chat/completions` answers `400 Failed to tokenize prompt` and the
  runner falls back to `/completion` with the server's `media_marker`. Same
  image, same question, same `temperature: 0`.
- The GPU was used through the device plugin grant (`amd.com/render: 1`) with
  `-ngl 99`. One arm at a time, enforced by a `ResourceQuota` capping that
  resource at 1 in `vlm-exp` and by `run_arm.sh` refusing to start while another
  arm's Pod is Running.

## Reproduce

```bash
cd benchmark/experiments/e5-vlm-grounding
kubectl -n registry create configmap ctx-vlm-server \
  --from-file=Containerfile=vlm-server/Containerfile
kubectl apply -f manifests/kaniko-vlm-server.yaml
kubectl -n registry wait --for=condition=complete job/kaniko-vlm-server --timeout=1800s

kubectl apply -f manifests/namespace.yaml
kubectl apply -f manifests/vlm-arms.yaml
bash run_arm.sh smolvlm2; bash run_arm.sh moondream; bash run_arm.sh qwen3vl
```

Each `run_arm.sh` invocation writes `results/<arm>.log`; the scores above come
from those three files and nothing else.

## Incidents worth remembering

| Symptom | Cause | Fix carried in the repo |
|---|---|---|
| Kaniko OOMKilled at 400/550 objects | 4Gi ceiling for a 6-way llama.cpp build | 6Gi limit in `manifests/kaniko-vlm-server.yaml` |
| `e5-frames` ConfigMap rejected | three PNGs are 390 KB, ConfigMap cap is 256 KiB | frames moved to the `vlm-frames` PVC |
| `ImagePullBackOff` on `192.168.0.28:5000` | containerd has no insecure entry for that address | images referenced as `127.0.0.1:5000/vlm-server:local` |
| SmolVLM2 7/7 unparseable | format instruction came after the question | instruction first, `max_tokens: 320` |
| Moondream 7/7 `400` | vicuna template cannot place an image part | `/completion` + `media_marker` fallback |
| Qwen3-VL curl 404 | the repo publishes only `Q8_0`, no `Q4_K_M` | manifest pins `Qwen3-VL-2B-Instruct-Q8_0.gguf` |
| moondream refusal to run once | the guard counted a Completed runner Pod as a live arm | guard filters to Running/Pending |

A caption-level 500M model and a 1.9B model with a weak chat template are both
worse than useless for driving a browser. The 2B Qwen3-VL at least answers the
question in the right shape; whether its 52 px miss and its candidate-list habit
can be prompted away is the next thing to measure.
