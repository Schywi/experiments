#!/usr/bin/env python3
"""One controller, five deterministic stages, the model at exactly one of them.

    python3 controller/controller.py --sets baseline --arm vision-only
    python3 controller/controller.py --sets baseline,discriminate --arm dom-candidates
    python3 controller/controller.py --sets baseline --arm dom-unique --dry-run

Stages, each writing one or more events.jsonl lines:

    1 acquire     claim a sandbox from the pool, record its identity
    2 connect     prove the DevTools bridge answers, record how long it took
    3 inspect     Runtime.evaluate + Accessibility.getFullAXTree + a frame
    4 resolve     DOM first; Qwen only where the DOM is ambiguous
    5 act         validate the point, click it, then prove what the click hit
    6 verify      compare href, scrollY, AX tree, frame hash and the tile's
                  screenshot_ts before and after

`--dry-run` performs stages 1-4 and stops before any dispatch, printing the
resolved target, the validation verdict and the element `elementFromPoint` would
return. A dry run writes no `browser.action.*` event, and the scorer treats a
dispatch in a dry run as a harness bug.
"""

from __future__ import annotations

import argparse
import datetime
import json
import os
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import console  # noqa: E402
import events as events_mod  # noqa: E402
import transform  # noqa: E402
import vlm  # noqa: E402

ROOT = os.path.dirname(HERE)
TASKS = os.path.join(ROOT, "tasks")
CONSOLE_HOME = os.environ.get("CONSOLE_HOME", "http://192.168.0.250:8080/")
SETTLE_SECONDS = float(os.environ.get("SETTLE_SECONDS", "1.0"))
# When 1, a vision coordinate is clicked even though the calibration fit is
# unusable. Off by default so a run cannot silently click a point 214 px from its
# target; on when the point of the run is to see where such a click lands.
ALLOW_VISION_CLICKS = os.environ.get("ALLOW_VISION_CLICKS", "0").strip() == "1"


# --------------------------------------------------------------------------- #
# helpers
# --------------------------------------------------------------------------- #
def now_run_id() -> str:
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")


def load_set(name: str) -> tuple[dict, dict, str]:
    set_dir = os.path.join(TASKS, name)
    with open(os.path.join(set_dir, "tasks.json"), encoding="utf-8") as fh:
        spec = json.load(fh)
    with open(os.path.join(set_dir, "labels.json"), encoding="utf-8") as fh:
        labels = json.load(fh)
    return spec, labels, set_dir


def frame_path(set_dir: str, spec: dict, frame_key: str) -> str:
    return os.path.normpath(os.path.join(set_dir, spec["frames"][frame_key]))


def selector_count(sandbox_id: str, selector: str) -> int:
    """How many elements currently match, and how many of those are visible.

    Uniqueness is the whole basis of the `dom-unique` arm, so it is measured
    rather than assumed: a selector that matches one hidden and one visible node is
    not unique for a click.
    """
    expression = (
        "(() => { const all = Array.from(document.querySelectorAll(%s));"
        " const visible = all.filter(e => { const r = e.getBoundingClientRect();"
        " return r.width > 0 && r.height > 0 && r.bottom > 0 && r.top < innerHeight"
        " && r.right > 0 && r.left < innerWidth; });"
        " return JSON.stringify({total: all.length, visible: visible.length}); })()"
        % json.dumps(selector)
    )
    value = console.evaluate(sandbox_id, expression)
    data = json.loads(value) if value else {"total": 0, "visible": 0}
    return int(data.get("visible", 0))


def selector_boxes(sandbox_id: str, selector: str) -> list[dict]:
    """Visible boxes for a selector, as click targets with their own identity."""
    expression = (
        "(() => { const out = []; const all = Array.from(document.querySelectorAll(%s));"
        " all.forEach((e, i) => { const r = e.getBoundingClientRect();"
        " if (!(r.width > 0 && r.height > 0 && r.bottom > 0 && r.top < innerHeight)) return;"
        " out.push({index: i, x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2),"
        " left: Math.round(r.x), top: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height),"
        " text: (e.textContent||'').trim().replace(/\\s+/g,' ').slice(0,60),"
        " id: (e.dataset && e.dataset.id) || '',"
        " act: (e.dataset && e.dataset.act) || ''}); });"
        " return JSON.stringify(out); })()"
        % json.dumps(selector)
    )
    value = console.evaluate(sandbox_id, expression)
    return json.loads(value) if value else []


def inside_box(point: tuple[float, float], box: dict, slack: int = 2) -> bool:
    x, y = point
    return (box["left"] - slack <= x <= box["left"] + box["w"] + slack
            and box["top"] - slack <= y <= box["top"] + box["h"] + slack)


def validate(answer: dict | None, *, box: dict | None, fit: dict,
             source: str = "vision") -> dict:
    """Stage 4's gate. Every rejection is structured, specific and non-lossy.

    Codes: no_json, abstained, coordinate_not_a_number (a candidate list),
    coordinate_missing, out_of_frame, calibration_unusable, outside_target_box, ok.

    A list of candidate points is `coordinate_not_a_number`, never a silent pick of
    the first: averaging or `[0]`-ing turns a formatting failure into a wrong click,
    which is exactly the failure E5 could not see.

    `source="dom"` means the point came from the accessibility tree rather than from
    the model. Those points are already in viewport pixels, so the calibration fit is
    not applied to them — and a calibration that is unusable therefore does not
    disqualify them, which is the whole reason for having a DOM path.
    """
    verdict = {"ok": False, "code": "", "detail": "", "source": source,
               "raw_point": None, "corrected_point": None,
               "inside_box": None, "inside_frame": None}
    if answer is None:
        verdict.update(code="no_json", detail="reply contained no JSON object")
        return verdict
    if "target" in answer and answer.get("target") is None and "x" not in answer and "y" not in answer:
        verdict.update(code="abstained", detail=str(answer.get("reason") or "target null"))
        return verdict
    raw_x, raw_y = answer.get("x"), answer.get("y")
    if isinstance(raw_x, list) or isinstance(raw_y, list):
        verdict.update(code="coordinate_not_a_number",
                       detail=f"x={raw_x!r} y={raw_y!r} are candidate lists")
        return verdict
    if not isinstance(raw_x, (int, float)) or not isinstance(raw_y, (int, float)) \
            or isinstance(raw_x, bool) or isinstance(raw_y, bool):
        verdict.update(code="coordinate_missing", detail=f"x={raw_x!r} y={raw_y!r}")
        return verdict
    raw = (float(raw_x), float(raw_y))
    verdict["raw_point"] = raw
    verdict["inside_frame"] = transform.inside(raw)
    if not verdict["inside_frame"]:
        verdict.update(code="out_of_frame", detail=f"({raw[0]},{raw[1]}) outside 1280x600")
        return verdict
    if source == "vision":
        if not transform.usable(fit) and not ALLOW_VISION_CLICKS:
            corrected = ((fit or {}).get("corrected") or {}).get("rms_px")
            verdict.update(
                code="calibration_unusable",
                detail=f"calibration status={(fit or {}).get('status')} "
                       f"corrected_rms={corrected}px: a vision coordinate cannot be trusted")
            return verdict
        # ALLOW_VISION_CLICKS=1: click whatever the model pointed at. The fit is
        # applied when it is usable and the raw point used when it is not, and the
        # landing is recorded either way — a misfire is a result, not a blocked run.
        corrected = transform.apply(fit, *raw)["corrected"] if transform.usable(fit) else raw
    else:
        corrected = raw
    verdict["corrected_point"] = corrected
    if box is not None:
        verdict["inside_box"] = inside_box(corrected or raw, box)
        if not verdict["inside_box"]:
            verdict.update(code="outside_target_box",
                           detail=f"point {corrected or raw} not in box "
                                  f"({box['left']},{box['top']},{box['w']}x{box['h']})")
            return verdict
    verdict.update(ok=True, code="ok")
    return verdict


def retry_prompt(task: dict, previous: dict, verdict: dict) -> str:
    """One retry, carrying the reason the previous answer was rejected.

    The rejection is the evidence: the model is told which rule it broke and the
    exact numbers that broke it, rather than being asked again in the hope of a
    different sample.
    """
    return (
        f"{task['ask']}\n"
        f"Your previous answer {json.dumps(previous)} was rejected: "
        f"{verdict['code']} — {verdict['detail']}.\n"
        'Answer again with exactly one JSON object and nothing else: '
        '{"target": "<name>", "x": <integer>, "y": <integer>}, or '
        '{"target": null, "reason": "<why the control is not on the page>"}.'
    )


# --------------------------------------------------------------------------- #
# stages
# --------------------------------------------------------------------------- #
def stage_acquire(ev: events_mod.Events, url: str) -> tuple[str, dict]:
    ev.emit("sandbox.acquire.started", detail={"url": url})
    started = time.perf_counter()
    sandbox_id = console.claim(url)
    elapsed = (time.perf_counter() - started) * 1000
    info = console.tile(sandbox_id)
    ev.emit("sandbox.acquire.succeeded", sandbox_id=sandbox_id, duration_ms=elapsed,
            detail={"claim": info.get("claim"), "phase": info.get("phase"),
                    "pod_ip": info.get("pod_ip"), "launch_type": info.get("launch_type"),
                    "age_s": info.get("age_s"), "url": info.get("url")})
    return sandbox_id, info


def stage_connect(ev: events_mod.Events, sandbox_id: str) -> dict:
    ev.emit("browser.connect.started", sandbox_id=sandbox_id)
    started = time.perf_counter()
    version = console.cdp(sandbox_id, "Browser.getVersion", {})
    elapsed = (time.perf_counter() - started) * 1000
    ev.emit("browser.connect.succeeded", sandbox_id=sandbox_id, duration_ms=elapsed,
            detail={"product": version.get("product"), "protocol": version.get("protocolVersion"),
                    "method": "router /cdp with the pod's ServiceAccount token"})
    return version


def stage_inspect(ev: events_mod.Events, sandbox_id: str, *, arm: str, task: str,
                  timeout: float = 60) -> dict:
    """Stage 2: evaluate, read the accessibility tree, capture a frame.

    The frame is taken twice, over both routes the stack offers — `Page.captureScreenshot`
    through the router with the pod's token, and the console's `/shot` route, which
    ends in the same DevTools call. Recording both hashes proves the controller is
    looking at the same Chromium the pane is, and catches a viewport or surface
    difference between the two paths.
    """
    started = time.perf_counter()
    state = console.page_state(sandbox_id)
    nodes = console.ax_nodes(sandbox_id)
    frame = console.screenshot_via_cdp(sandbox_id, timeout=timeout)
    console_frame = console.shot(sandbox_id, fresh=True, timeout=timeout)
    elapsed = (time.perf_counter() - started) * 1000
    cdp_sha, console_sha = console.sha256(frame), console.sha256(console_frame)
    detail = {"url": state.get("url"), "scrollY": state.get("scrollY"),
              "viewport": [state.get("w"), state.get("h")],
              "ax_nodes": len(nodes),
              "frame_sha256": cdp_sha, "frame_bytes": len(frame),
              "console_frame_sha256": console_sha,
              "frames_agree": cdp_sha == console_sha}
    ev.emit("browser.inspect.completed", sandbox_id=sandbox_id, arm=arm, task=task,
            duration_ms=elapsed, detail=detail)
    return {"state": state, "nodes": nodes, "frame": frame,
            "frame_sha256": cdp_sha, "console_frame_sha256": console_sha}


def ax_matches(nodes: list[dict], needle: str) -> list[dict]:
    """Accessibility nodes whose name contains `needle`, case-insensitively.

    The tree is the source of truth for "is this control on the page": a node that
    is ignored or unnamed cannot be clicked, so it cannot be a match.
    """
    needle = (needle or "").strip().lower()
    if not needle:
        return []
    return [
        node for node in nodes
        if not node.get("ignored") and needle in str(node.get("name") or "").lower()
    ]


def stage_resolve(ev: events_mod.Events, *, sandbox_id: str, arm: str, task: dict,
                  label: dict, frozen_frame: bytes, fit: dict,
                  nodes: list[dict] | None = None) -> dict:
    """Stage 3: resolve the target, and record which route answered.

    `dom-unique`  exactly one visible accessibility node matches -> the model is not
                  called at all, which is the measurement of how much work the model
                  was never needed for.
    `dom-candidates`  two or more visible matches -> the model chooses among them,
                  each candidate carrying its name, box and backendNodeId.
    `vision-only`  the DOM is not consulted; the model sees the frozen frame, exactly
                  as in E5.

    A candidate list from the model is never reduced to one point by the harness;
    `validate()` rejects it and the task records why.
    """
    started = time.perf_counter()
    nodes = nodes if nodes is not None else console.ax_nodes(sandbox_id)
    needle = label.get("ax_match") or ""
    matches = ax_matches(nodes, needle)
    boxes: list[dict] = []
    for node in matches:
        backend_id = node.get("backendDOMNodeId")
        if backend_id is None:
            continue
        box = console.box_for_backend_node(sandbox_id, int(backend_id))
        if box and box.get("visible"):
            boxes.append({**box, "backendNodeId": int(backend_id),
                          "role": node.get("role"), "name": node.get("name")})
    for box in boxes:
        box["text"] = box.get("name") or box.get("text") or ""

    resolved: dict = {"arm": arm, "resolved_by": "", "point": None, "answer": None,
                      "qwen_called": False, "candidates": boxes, "box": None,
                      "ax_match": needle, "ax_matches": len(matches),
                      "ax_visible": len(boxes)}

    if arm == "dom-unique" and len(boxes) == 1:
        box = boxes[0]
        resolved.update(resolved_by="dom-unique", point=(box["x"], box["y"]), box=box)
    elif arm == "dom-candidates" and len(boxes) >= 1:
        prompt = (
            "You are choosing among candidate controls on a web page. "
            f"{task['ask']}\n"
            "Candidates, each with its accessibility name and its box in pixels:\n"
            + "\n".join(
                f"  [{i}] name={b['name']!r} role={b['role']} "
                f"box=({b['left']},{b['top']},{b['w']}x{b['h']})"
                for i, b in enumerate(boxes)
            )
            + "\nAnswer with exactly one JSON object and nothing else: "
              '{"target": "<the name you chose>", "x": <integer>, "y": <integer>}.'
        )
        result = vlm.ask(prompt, _b64(console.screenshot_via_cdp(sandbox_id)))
        resolved.update(qwen_called=True, answer=result["answer"], raw=result["raw"],
                        wall_ms=result["wall_ms"], error=result["error"],
                        resolved_by="dom-candidates")
        if result["answer"]:
            point = _point_of(result["answer"])
            for box in boxes:
                if point and inside_box(point, box):
                    resolved["box"] = box
                    break
    elif arm == "dom-unique":
        resolved.update(resolved_by="dom-not-unique" if boxes else "dom-no-match")

    if arm == "vision-only":
        result = vlm.ask(_vision_prompt(task, frozen_frame), _b64(frozen_frame))
        resolved.update(qwen_called=True, answer=result["answer"], raw=result["raw"],
                        wall_ms=result["wall_ms"], error=result["error"],
                        resolved_by="vision-only")

    # Stage 4's gate. A DOM-resolved point is already in viewport pixels; anything
    # the model said is a vision coordinate and has to survive the calibration rule.
    if resolved["resolved_by"] == "dom-unique" and resolved["point"]:
        verdict = validate({"x": resolved["point"][0], "y": resolved["point"][1]},
                           box=resolved["box"], fit=fit, source="dom")
        resolved["validation"] = verdict
        if not verdict["ok"]:
            resolved["point"] = None
    elif resolved["point"] is None:
        verdict = validate(resolved["answer"], box=resolved["box"], fit=fit, source="vision")
        resolved["validation"] = verdict
        if verdict["ok"]:
            resolved["point"] = verdict["corrected_point"] or verdict["raw_point"]
        elif resolved["qwen_called"] and verdict["code"] not in ("abstained", "no_json"):
            # One retry, carrying the rule that was broken and the numbers that broke
            # it. A retry with no evidence would just be a second sample.
            result = vlm.ask(retry_prompt(task, resolved["answer"], verdict),
                             _b64(frozen_frame) if arm == "vision-only"
                             else _b64(console.screenshot_via_cdp(sandbox_id)))
            retry = {"prompt_code": verdict["code"], "answer": result["answer"],
                     "raw": (result["raw"] or "")[:400], "wall_ms": result["wall_ms"],
                     "error": result["error"]}
            resolved["retry"] = retry
            resolved["answer"] = result["answer"]
            verdict = validate(result["answer"], box=resolved["box"], fit=fit, source="vision")
            resolved["validation"] = verdict
            if verdict["ok"]:
                resolved["point"] = verdict["corrected_point"] or verdict["raw_point"]
    if resolved["point"] is None:
        resolved["point"] = None

    elapsed = (time.perf_counter() - started) * 1000
    ev.emit("agent.target.resolved", sandbox_id=sandbox_id, arm=arm, task=task["id"],
            ok=bool(resolved["point"]), duration_ms=elapsed,
            detail={"resolved_by": resolved["resolved_by"],
                    "qwen_called": resolved["qwen_called"],
                    "ax_match": needle, "ax_matches": len(matches),
                    "ax_visible": len(boxes),
                    "candidates": [{"name": b["name"], "box": [b["left"], b["top"], b["w"], b["h"]],
                                    "backendNodeId": b["backendNodeId"]} for b in boxes][:12],
                    "point": resolved["point"],
                    "validation": resolved.get("validation"),
                    "retry": resolved.get("retry"),
                    "answer": resolved["answer"],
                    "raw": (resolved.get("raw") or "")[:400],
                    "wall_ms": resolved.get("wall_ms")})
    return resolved


def _point_of(answer: dict) -> tuple[float, float] | None:
    x, y = answer.get("x"), answer.get("y")
    if isinstance(x, (int, float)) and isinstance(y, (int, float)) \
            and not isinstance(x, bool) and not isinstance(y, bool):
        return float(x), float(y)
    return None


def stage_act(ev: events_mod.Events, *, sandbox_id: str, arm: str, task: dict,
              resolved: dict, before: dict, dry_run: bool) -> dict:
    """Stage 5: click, then prove what the click hit.

    The proof is a set of independent observations, not one:
      - `elementFromPoint` and `DOM.getNodeForLocation` at the point, read BEFORE the
        dispatch, so the intention is on record even if the click kills the sandbox
      - the accessibility tree before and after, as a count and as the set of names
        that appeared or disappeared
      - `location.href` and `scrollY` before and after
      - the frame's sha256 before and after, and whether it changed
      - the tile's `screenshot_ts` before and after, which is the property the pane's
        own preview keys on
    """
    point = resolved["point"]
    at_point = console.element_at(sandbox_id, point[0], point[1]) if point else None
    node = console.node_at(sandbox_id, point[0], point[1]) if point else None
    ts_before = (console.tile(sandbox_id).get("screenshot_ts") if point else None)
    plan = {"point": point, "element_at_point": at_point, "node_at_point": node,
            "intended_backend_node_id": (resolved.get("box") or {}).get("backendNodeId"),
            "url_before": (before.get("state") or {}).get("url"),
            "scrollY_before": (before.get("state") or {}).get("scrollY"),
            "frame_before_sha256": before["frame_sha256"],
            "screenshot_ts_before": ts_before,
            "dry_run": dry_run}
    if dry_run:
        ev.emit("browser.action.rejected", sandbox_id=sandbox_id, arm=arm, task=task["id"],
                ok=False, detail={**plan, "reason": "dry run: no dispatch"})
        return plan
    if not point:
        ev.emit("browser.action.rejected", sandbox_id=sandbox_id, arm=arm, task=task["id"],
                ok=False, detail={**plan, "reason": "no validated point"})
        return plan

    started = time.perf_counter()
    ev.emit("browser.action.started", sandbox_id=sandbox_id, arm=arm, task=task["id"],
            detail=plan)
    try:
        console.click(sandbox_id, point[0], point[1])
        console.wait_for(SETTLE_SECONDS)
        elapsed = (time.perf_counter() - started) * 1000
        after_state = console.page_state(sandbox_id)
        after_nodes = console.ax_nodes(sandbox_id)
        after_frame = console.shot(sandbox_id, fresh=True)
        after_tile = console.tile(sandbox_id)
        before_names = {n["name"] for n in (before.get("nodes") or []) if n.get("name")}
        after_names = {n["name"] for n in after_nodes if n.get("name")}
        landed_on_intended = (
            node is not None
            and plan["intended_backend_node_id"] is not None
            and node.get("backendNodeId") == plan["intended_backend_node_id"]
        )
        out = {
            **plan,
            "duration_ms": round(elapsed, 1),
            "landed_on_intended_node": landed_on_intended,
            "after_url": after_state.get("url"),
            "after_scrollY": after_state.get("scrollY"),
            "url_changed": after_state.get("url") != plan["url_before"],
            "scrollY_changed": after_state.get("scrollY") != plan["scrollY_before"],
            "ax_nodes_after": len(after_nodes),
            "ax_names_added": sorted(after_names - before_names)[:10],
            "ax_names_removed": sorted(before_names - after_names)[:10],
            "ax_changed": after_names != before_names,
            "frame_after_sha256": console.sha256(after_frame),
            "frame_changed": console.sha256(after_frame) != before["frame_sha256"],
            "screenshot_ts_after": after_tile.get("screenshot_ts"),
            "screenshot_ts_changed": after_tile.get("screenshot_ts") != ts_before,
        }
        ev.emit("browser.action.completed", sandbox_id=sandbox_id, arm=arm, task=task["id"],
                duration_ms=elapsed, detail=out)
        ev.emit("preview.update.observed", sandbox_id=sandbox_id, arm=arm, task=task["id"],
                ok=bool(out["frame_changed"] or out["screenshot_ts_changed"]),
                detail={"frame_changed": out["frame_changed"],
                        "before_sha256": before["frame_sha256"],
                        "after_sha256": out["frame_after_sha256"],
                        "screenshot_ts_before": ts_before,
                        "screenshot_ts_after": out["screenshot_ts_after"],
                        "ax_changed": out["ax_changed"],
                        "url_changed": out["url_changed"]})
        return out
    except console.ConsoleError as error:
        elapsed = (time.perf_counter() - started) * 1000
        ev.emit("browser.action.completed", sandbox_id=sandbox_id, arm=arm, task=task["id"],
                ok=False, duration_ms=elapsed,
                detail={**plan, "error": str(error), "status": error.status,
                        "sandbox_lost": error.status in (404, 502, 503)})
        return {**plan, "error": str(error)}


# --------------------------------------------------------------------------- #
# prompts
# --------------------------------------------------------------------------- #
def _b64(payload: bytes) -> str:
    import base64
    return base64.b64encode(payload).decode()


def _vision_prompt(task: dict, frame: bytes) -> str:
    return (
        "The image is a 1280x600 pixel screenshot of a web page. "
        f"{task['ask']} If the control is not visible anywhere on the page, answer "
        '{"target": null, "reason": "<why>"} instead of a coordinate. '
        + vlm.POINT_INSTRUCTION
    )


# --------------------------------------------------------------------------- #
# main
# --------------------------------------------------------------------------- #
def run_task(ev: events_mod.Events, *, arm: str, set_name: str, task: dict, label: dict,
             frozen_frame: bytes, fit: dict, page_url: str, sandbox_id: str | None,
             dry_run: bool) -> tuple[str | None, dict]:
    """One task, all stages. Returns the sandbox in use, and the task's outcome."""
    if sandbox_id is None:
        sandbox_id, _ = stage_acquire(ev, page_url)
        stage_connect(ev, sandbox_id)
    else:
        console.navigate(sandbox_id, page_url)
    before = stage_inspect(ev, sandbox_id, arm=arm, task=task["id"])
    resolved = stage_resolve(ev, sandbox_id=sandbox_id, arm=arm, task=task,
                             label=label, frozen_frame=frozen_frame, fit=fit)
    action = stage_act(ev, sandbox_id=sandbox_id, arm=arm, task=task, resolved=resolved,
                       before=before, dry_run=dry_run)
    outcome = {"set": set_name, "task": task["id"], "arm": arm,
               "resolved_by": resolved["resolved_by"],
               "qwen_called": resolved["qwen_called"],
               "point": resolved["point"],
               "validation": resolved.get("validation"),
               "action": action,
               "frame_before_sha256": before["frame_sha256"]}
    ev.emit("experiment.completed", sandbox_id=sandbox_id, arm=arm, task=task["id"],
            ok=bool(resolved["point"]), detail={"set": set_name, **outcome})
    return sandbox_id, outcome


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--sets", default="baseline", help="comma-separated task set names")
    parser.add_argument("--arm", default="vision-only",
                        choices=["vision-only", "dom-unique", "dom-candidates"])
    parser.add_argument("--out", default=os.path.join(ROOT, "results"))
    parser.add_argument("--run-id", default=None)
    parser.add_argument("--model-url", default=os.environ.get("MODEL_URL", "http://127.0.0.1:8000"))
    parser.add_argument("--fit", default=os.path.join(ROOT, "calibration", "fit.json"))
    parser.add_argument("--page-url", default=CONSOLE_HOME)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    vlm.MODEL_URL = args.model_url.rstrip("/")
    run_id = args.run_id or now_run_id()
    run_dir = os.path.join(args.out, "e6", run_id)
    ev = events_mod.Events(os.path.join(run_dir, "events.jsonl"), run_id)
    fit = transform.load(args.fit) if os.path.exists(args.fit) else {}

    print(f"[{run_id}] arm={args.arm} dry_run={args.dry_run} sets={args.sets}", flush=True)
    print(f"[{run_id}] {transform.describe(fit)}", flush=True)

    sandbox_id: str | None = None
    outcomes: list[dict] = []
    created_by_us = False
    try:
        for set_name in [s.strip() for s in args.sets.split(",") if s.strip()]:
            spec, labels, set_dir = load_set(set_name)
            for task in spec["tasks"]:
                label = (labels.get("tasks") or {}).get(task["id"]) or {}
                frame = frame_path(set_dir, spec, task["frame"])
                with open(frame, "rb") as fh:
                    frozen = fh.read()
                try:
                    sandbox_id, outcome = run_task(
                        ev, arm=args.arm, set_name=set_name, task=task, label=label,
                        frozen_frame=frozen, fit=fit, page_url=args.page_url,
                        sandbox_id=sandbox_id, dry_run=args.dry_run)
                    created_by_us = created_by_us or sandbox_id is not None
                    outcomes.append(outcome)
                    print(f"  {set_name}/{task['id']}: resolved_by={outcome['resolved_by']} "
                          f"point={outcome['point']} qwen={outcome['qwen_called']}", flush=True)
                except console.ConsoleError as error:
                    ev.emit("experiment.aborted", sandbox_id=sandbox_id, arm=args.arm,
                            task=task["id"], ok=False, detail={"error": str(error)})
                    print(f"  {set_name}/{task['id']}: ABORTED {error}", flush=True)
                    sandbox_id = None  # a lost sandbox is re-acquired for the next task
    finally:
        if sandbox_id and created_by_us and not args.dry_run:
            try:
                console.release(sandbox_id)
            except console.ConsoleError:
                pass
        ev.close()

    print(f"\n[{run_id}] {len(outcomes)} task(s) done; events at "
          f"{os.path.join(run_dir, 'events.jsonl')}", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
