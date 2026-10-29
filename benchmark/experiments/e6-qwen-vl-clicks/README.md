# E6 — Qwen3-VL-2B drives a real Chromium and clicks

- **Status:** **aborted 2026-10-28** — see [`POSTMORTEM.md`](POSTMORTEM.md). The
  harness works and five runs are recorded; the scored comparison does not exist,
  and the work is not being continued (time and resources).
- **Scope:** one namespace (`vlm-e6`), one experiment directory
  (`benchmark/experiments/e6-qwen-vl-clicks/`). Nothing under `apps/` or `config/`
  changes. E5's directory (`../e5-vlm-grounding/`) is frozen and is read, never
  written.
- **Model:** Qwen3-VL-2B-Instruct Q8_0 only, the arm E5 measured at 1/7. No second
  model is deployed, and no comparison between models is made here.

## What E5 left open

E5 asked three VLMs to name a control and give its coordinate from a 1280x600 PNG.
Qwen3-VL answered valid JSON on 7/7 tasks and was right once. Two specific
questions survived that run, and this experiment exists to answer them:

1. **Is its coordinate error a coordinate-space problem?** On task 2 it reported
   (38,145) for a button whose centre is (58,97): dx -20, dy +48. If the frame the
   model sees is rescaled relative to the frame the browser paints, a fixed
   transform would correct every future coordinate. If it is not, vision-only
   grounding on this model is finished and the DOM has to do the work.
2. **Does a click at its coordinate do the intended thing?** E5 never clicked.
   Nothing in that run distinguishes "point 52 px off" from "point on the wrong
   element", because neither was executed.

## Clicks are real and unrestricted

Every action dispatched in this experiment is a real `Input.dispatchMouseEvent`
through the console's own path (`POST /api/sandboxes/{id}/click {x, y, scale: 1}`
-> `cdp.py`'s `click()` -> `mousePressed`/`mouseReleased` -> `settle()`). The task
set includes the console's `delete` buttons, which destroy the sandbox whose card
they belong to. Nothing is filtered, disabled or made inert.

Consequences the harness has to handle, and does:

- A destructive click changes the page for every later task, so each task restores
  its frame first: navigate to the URL, re-apply the scroll offset, re-capture, and
  only then ask anything.
- A click can delete the sandbox the controller is talking to. The controller
  detects an unreachable sandbox, records it, claims a replacement, and continues;
  the run's created claims are deleted at teardown.
- `page.setViewport`/`Emulation` are never used, so the 1280x600 viewport is the
  one the console's own tiles use, and image pixels equal CSS pixels.

## Three resolution arms, one model call each at most

Every task is run under one arm at a time, and the arm is recorded per event.

| Arm | How the target is found | Qwen called |
|---|---|---|
| `vision-only` | nothing but the PNG, exactly E5's method | always |
| `dom-unique` | `Accessibility.getFullAXTree`, one visible node matches the asked name | never |
| `dom-candidates` | 2+ visible matches; Qwen picks one and gives its point, with each candidate's name, `backendNodeId` and box in the prompt | once per task |

The point of `dom-unique` is to measure how many tasks the model was never needed
for. If the DOM resolves the target uniquely, asking a 2B model to guess
coordinates is a way to introduce error, not to reduce it.

## The six metrics, kept apart

Nothing is rolled into "accuracy". Per task, per arm:

| Metric | Definition |
|---|---|
| `schema_valid` | reply parses as one JSON object with a single numeric `x` and `y` inside 1280x600. Arrays, strings, missing fields and out-of-bounds are separate error codes |
| `target_correct` | the reported target identity matches the asked target (normalised label match, or the chosen `backendNodeId` when the DOM offered candidates) |
| `coord_error_px` | distance from the reported point to the ground-truth point, raw and after calibration |
| `click_landed` | `document.elementFromPoint(x, y)` before dispatch, and the AX node at that point, are the intended element |
| `visible_change` | `location.href`, `scrollY`, the AX tree, and the frame's `sha256` all compared before and after, plus the tile's `screenshot_ts` from `GET /api/sandboxes/{id}` |
| `abstained_correctly` | on the three negative tasks, the reply is `{"target": null, ...}` instead of an invented point |

## Lifecycle events

One JSON object per line in `results/e6/<run-id>/events.jsonl`. Every event carries
`ts` (ISO 8601), `run_id`, `sandbox_id`, `arm`, `task`, `ok`, and `duration_ms` where
the stage has a duration.

```
sandbox.acquire.started      sandbox.acquire.succeeded     sandbox.acquire.failed
browser.connect.started      browser.connect.succeeded     browser.connect.failed
browser.inspect.completed
agent.target.resolved
browser.action.started       browser.action.completed      browser.action.rejected
preview.update.observed
experiment.completed
```

`browser.action.rejected` is the contract failure path: the reply was parsed but
does not satisfy the schema or the box check, so no click was dispatched. A
`browser.action.started` without a matching `browser.action.completed` is a bug in
the controller, and the scorer treats it as one.

## Transport

The controller talks to the console over the console's own HTTP API and gets at
Chromium through the console's router path, exactly as the pane does:

```
controller --HTTP--> sandbox-console.sandbox-console.svc.cluster.local:8080
             --route--> sandbox-router-svc.agent-sandbox-system:8080
                        X-Sandbox-ID, X-Sandbox-Port: 9222,
                        X-Sandbox-Namespace: sandbox-demo, Bearer <SA token>
             --HTTP--> pod:9222 (cdp-bridge.py) --WS--> 127.0.0.1:9223 Chromium
```

`POST /cdp {"method": ..., "params": ...}` accepts any DevTools method, so the
controller uses `Runtime.evaluate`, `Accessibility.enable`,
`Accessibility.getFullAXTree`, `DOM.getBoxModel` and `Page.captureScreenshot`
through the same bridge the console uses. The router runs
`--authz-mode=tokenreview`, so the Job's own projected ServiceAccount token is the
credential.

## Fixtures

The calibration page is served from a pod in `vlm-e6`, because the console's
`/static/` is fixed at build time and `apps/` is out of scope. Sandbox egress
already allows any pod in the cluster (`config/sandbox/templates/lan-egress-networkpolicy.yaml`),
so the sandbox can render it.

| Page | Contents |
|---|---|
| `harness/page/calibration.html` | 25 crosses at x = 128, 384, 640, 896, 1152 and y = 60, 180, 300, 420, 540, each printed with its coordinate, plus four corner rulers |
| `harness/page/targets.html` | buttons with known boxes for the click tests, including four identical delete buttons that differ only by adjacent id text |

## Run

```bash
cd benchmark/experiments/e6-qwen-vl-clicks
bash run_e6.sh --dry-run     # stages 1-3 for all tasks, no click dispatched
bash run_e6.sh               # the whole run, real clicks
```

`run_e6.sh` refuses to start while any other model Pod is Running or Pending, scales
`qwen3vl` to 1, waits for Ready, runs the controller Job, writes
`results/e6/<run-id>/`, and scales back to 0 even when the run fails.

## Teardown

```bash
kubectl delete ns vlm-e6
```

Claims the run created are deleted by the controller; the pushed image
`127.0.0.1:5000/vlm-server:local` stays in the registry.

## Related

- `../e5-vlm-grounding/` — the frozen baseline this reuses: same three frames, same
  seven tasks, same 32 px radius, same Qwen3-VL build.
- `research/features/sandbox-agent_experiment.md` — the console's route table with
  measured costs.
