# E6 postmortem — Qwen3-VL-2B driving a real Chromium, aborted

- **Status:** aborted 2026-10-28. No further development planned.
- **Reason:** time and resources. The harness works and produced measurements; the
  remaining work — one live click path per arm, scored and written up — did not fit
  the session, and continuing would have cost more model startups than the
  experiment is worth.
- **What is left behind:** a working controller, five recorded runs, a calibration
  fit that answers E5's open question, and `flow.sh`, which reproduces the whole
  thing end to end.

Nothing in `apps/` or `config/` was modified. The namespace `vlm-e6` was deleted.

## What was built

| Path | What it is |
|---|---|
| `controller/controller.py` | Six stages: acquire, connect, inspect, resolve, act, verify. One model call at most per task |
| `controller/console.py` | The console's HTTP API plus DevTools over the router's `/cdp`, using the pod's own ServiceAccount token |
| `controller/transform.py` | Loads the calibration fit, decides whether it is usable, applies it |
| `controller/calibrate.py` | Fits `viewport = a·reported + b` against a known 5x5 grid, three reps |
| `controller/vlm.py`, `events.py` | The model call, and one JSON line per lifecycle event |
| `harness/probe_page.py` | Reads a live page's geometry through the console's CDP route, to build a task set |
| `harness/summarize.py` | Folds `events.jsonl` into a per-task table |
| `harness/check_tasks.py` | Asserts every task set is consistent with its frames and labels |
| `harness/page/targets.html` | The fixture page: known button boxes, `aria-label` carrying the sandbox id |
| `harness/page/calibration*.html` | The 5x5 cross grid for the coordinate fit |
| `flow.sh` | The whole flow in one script, every step printed, teardown on every exit path |
| `tasks/{baseline,discriminate,negative,determinism,targets}/` | 25 tasks over five sets |
| `manifests/` | Namespace, quota capping `amd.com/render` at 1, model Deployment, page server, controller Job |

`controller/` carries 33 unit tests (`cd controller && python3 -m unittest`).

## What was measured

Five runs, all recorded in `results/e6/*/events.jsonl`. Counts read from those
files with `harness/summarize.py`.

| Run | Arm | Page | Tasks | DOM-resolved | Qwen calls | Clicks | Landed on intended node | Frame changed |
|---|---|---|---|---|---|---|---|---|
| `20261028T202057Z` | dom-unique | E5's frozen console frames | 17 | 0 | 0 | 0 | — | — |
| `20261028T202325Z` | dom-unique | same, retried after adding the fixture page | 8 | 0 | 0 | 0 | — | — |
| `20261028T202453Z` | dom-unique | served `targets.html` | 8 | 7 | 0 | **7** | **7** | **7** |
| `20261028T202610Z` | dom-candidates | live console grid | 4 | 1 | 1 | 0 | — | — |
| `20261028T202719Z` | vision-only | served `targets.html` | 8 | 0 | 8 | 0 | — | — |

Totals: 45 task events, 7 clicks, 7 landings on the intended accessibility node,
7 frame changes, 17 `dom-no-match`, 9 model calls, 38 rejections.

## Finding 1 — the coordinate error is not a coordinate-space problem

`calibration/fit.json`, 24 usable pairs over the 5x5 grid, three reps:

```
x: viewport = 1.441683 * reported + 62.538
y: viewport = 2.639863 * reported - 20.863
raw       rms = 381.42 px   max = 706.55 px
corrected rms = 214.28 px   max = 604.59 px
status: unusable            stable across reps: True
```

The replies are quantised. The same point comes back for targets spread across the
whole frame — `[288,120]` for true x 384, 640 and 1152 at y 540; `[104,104]` for
both `(128,300)` and `(128,420)` — and y saturates near 120–150 for true y from 60
to 540. The model is answering in some downscaled space, so the error is not a
scale factor that a transform can undo. Fitting one anyway leaves 214 px rms, a
third of the viewport.

This closes E5's open question. Its 52 px miss on task 2 was a lucky sample of an
error that is unbounded: vision coordinates from this build cannot be placed on a
page.

## Finding 2 — the accessibility tree resolves and clicks, with the model not called

On the served page, 7 of 8 tasks resolved to exactly one accessible node, clicked
through the console's own `POST /api/sandboxes/{id}/click`, landed on the intended
`backendNodeId`, and changed the frame afterwards. Qwen was called zero times in
that run. The eighth task asked for a control that does not exist and was correctly
not resolved.

Example row, `t-tg-delete-65l7z`:

```
resolved_by=dom-unique  point=(436,165)  validation=ok (source=dom, inside_box=true)
element_at_point={"tag":"button","text":"delete","target":"delete-65l7z"}
node_at_point.backendNodeId=186  intended=186  landed_on_intended_node=true
frame_changed=true  screenshot_ts 1793219094 -> 1793219095
```

## Finding 3 — E5's frozen frames cannot drive a DOM arm

The first two runs resolved nothing: 17 `dom-no-match`. E6's baseline set reuses
E5's PNGs of six console cards, and those sandboxes were deleted after E5 captured
them, so the live page no longer contains the elements. A DOM arm needs a page that
is live at run time; the frames are evidence, not a fixture a controller can act
on. That is why `tasks/targets/` and the served page exist.

## What broke along the way

| Symptom | Cause | Where it is recorded |
|---|---|---|
| `dom-no-match` on all 17 baseline tasks | frozen frames vs live page | run `20261028T202057Z` |
| `dom-no-match` on the whole targets set once | controller navigates to one `--page-url` for every task; the set had no page of its own, so it inspected the console grid instead of the fixture page | run `20261028T202325Z`, fixed by `--page-url` |
| chromewebdata error page | the sandbox cannot resolve `cal-page.vlm-e6.svc.cluster.local`; the ClusterIP works | `harness/probe_page.py` docstring |
| `dom-candidates` aborted 3 of 4 tasks | the console grid's own delete button was clicked, destroying the sandbox the controller was talking to | run `20261028T202610Z` |
| vision-only dispatched no clicks | the controller's calibration gate refuses untrusted coordinates | run `20261028T202719Z`, switch `ALLOW_VISION_CLICKS` added |
| a stale second EXIT trap | would have replaced the teardown that scales the model to 0, leaving the single render device held | fixed in `run_e6.sh` before it ran |

## Why it was aborted

Each arm costs a model start: scale to 1, load 1.83 GB Q8_0 plus the 445 MB
projector, wait for `/health`, run, scale back to 0. That is minutes per arm, and
the three arms across two task sets is six of those. On top of it, every run claims
its own sandbox, a page-state mismatch forced a new task set mid-run, and shell
calls in this session are capped at two minutes, so the orchestration had to be
polled rather than observed.

The result is a harness that works, three findings, and no scored comparison. The
comparison was the point, and it is not there.

## If someone resumes

The pieces that work: the controller, the DOM path, the fixture page, the
calibration, `flow.sh`.

Order to pick it up in:

1. `cd benchmark/experiments/e6-qwen-vl-clicks && bash flow.sh --keep` — whole flow,
   namespace left up.
2. `bash flow.sh --only vision-only` — clicks the model's raw coordinates with the
   gate off; the interesting number is `landed_on_intended_node`, expected false.
3. `bash flow.sh --only dom-candidates --page-url http://10.43.231.227:8000/targets.html`
   — needs tasks whose `ax_match` matches more than one node, which the current
   `targets/` set does not have by design.
4. Replace `tasks/baseline/` frames with a capture taken in the same session as the
   run, or drop the set: a DOM arm cannot use them.
5. Write the scored comparison (`harness/summarize.py` already emits the table).

Two things not to repeat: do not click the console's own delete buttons while the
run still needs that sandbox, and do not add a second `trap … EXIT` after the
teardown trap.

## Teardown

`kubectl delete ns vlm-e6`. The image `127.0.0.1:5000/vlm-server:local` and the
`kaniko-cache-vlm` repo stay in the registry; both are E5's.

## Related

- `../e5-vlm-grounding/RESULTS.md` — the frozen baseline this reused.
- `../e5-vlm-grounding/` — same frames, same seven tasks, same Qwen3-VL build.
