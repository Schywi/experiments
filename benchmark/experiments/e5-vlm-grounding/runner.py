#!/usr/bin/env python3
"""E5 runner: one VLM arm, every task, the exact same frames.

The runner is invoked once per arm. It never learns about the other two arms, so
the arms cannot interleave: the sequencing lives in the job that starts it, and
the namespace quota refuses a second arm holding the render device.

Inputs (mounted by manifests/runner-job.yaml):

    FIXTURES/tasks.json    prompts + task kinds + which frame each uses
    FIXTURES/labels.json   ground truth: coordinates, accepted labels, tolerance
    FRAMES/<file>          the PNG bytes every arm is shown

Output, on stdout, as the e4-gpu-e2e runner already does it:

    ROW {json}          one line per task, printed as it finishes
    RESULT_JSON {json}  the whole run: rows, per-kind hit rates, latencies

Third-party packages: none. The image is python:3.12-slim and the cluster has no
package index the runner needs.

  ARM=qwen3vl MODEL_URL=http://vlm-qwen3vl:8000 \
    FIXTURES=fixtures FRAMES=results/frames python3 runner.py
"""

from __future__ import annotations

import base64
import json
import math
import os
import sys
import time
import urllib.error
import urllib.request

FIXTURES = os.environ.get("FIXTURES", "fixtures")
FRAMES = os.environ.get("FRAMES", "results/frames")
ARM = os.environ.get("ARM", "unknown")
MODEL_URL = os.environ.get("MODEL_URL", "http://127.0.0.1:8000").rstrip("/")
TIMEOUT = float(os.environ.get("TIMEOUT", "600"))
MAX_TOKENS = int(os.environ.get("MAX_TOKENS", "320"))
ATTEMPTS = int(os.environ.get("ATTEMPTS", "3"))

# The format instruction comes first and the question second: a 500M model that
# is asked for prose answers with prose, and the first version of this prompt
# put the ask first and hit `max_tokens` mid-sentence on every task — 7/7
# unparseable replies, which measured the prompt rather than the model.
POINT_INSTRUCTION = (
    'Answer with exactly one JSON object and nothing else, no prose: '
    '{"label": "<short name of what you found>", "x": <integer>, "y": <integer>}. '
    "x and y are pixel coordinates measured from the top-left corner of the image."
)
LABEL_INSTRUCTION = (
    'Answer with exactly one JSON object and nothing else, no prose: '
    '{"label": "<short name of what you found>"}.'
)


# --------------------------------------------------------------------------- #
# Pure helpers (covered by test_runner.py)
# --------------------------------------------------------------------------- #
def build_prompt(task: dict, width: int, height: int) -> str:
    """One prompt per task, identical across arms."""
    instruction = POINT_INSTRUCTION if task["kind"] in ("point", "label+point") else LABEL_INSTRUCTION
    return (
        f"The image is a {width}x{height} pixel screenshot of a web page. "
        f"{task['ask']} {instruction}"
    )


def first_json_object(text: str) -> dict | None:
    """The first balanced {...} in a reply, or None.

    Models wrap JSON in prose, in ```json fences, or emit two objects. Scanning
    for the first balanced object costs a few lines and keeps the parse honest:
    a reply that only *mentions* a coordinate is not an answer.
    """
    start = text.find("{")
    while start != -1:
        depth = 0
        in_string = False
        escape = False
        for index in range(start, len(text)):
            char = text[index]
            if in_string:
                if escape:
                    escape = False
                elif char == "\\":
                    escape = True
                elif char == '"':
                    in_string = False
                continue
            if char == '"':
                in_string = True
            elif char == "{":
                depth += 1
            elif char == "}":
                depth -= 1
                if depth == 0:
                    try:
                        parsed = json.loads(text[start:index + 1])
                    except json.JSONDecodeError:
                        break
                    return parsed if isinstance(parsed, dict) else None
        start = text.find("{", start + 1)
    return None


def normalise(value: object) -> str:
    """Lowercase alphanumerics only: 'Full-screen link' == 'fullscreen_link'."""
    return "".join(ch for ch in str(value or "").lower() if ch.isalnum())


def as_point(answer: dict | None) -> tuple[float, float] | None:
    if not answer:
        return None
    try:
        return float(answer["x"]), float(answer["y"])
    except (KeyError, TypeError, ValueError):
        return None


def score(task: dict, truth: dict, answer: dict | None, tolerance: float) -> dict:
    """Score one answer against the label file. Returns the row's scoring fields."""
    kind = task["kind"]
    row: dict = {
        "parsed": answer is not None,
        "label": None if answer is None else answer.get("label"),
    }
    if kind in ("label", "label+point"):
        accepted = [normalise(a) for a in truth.get("labels", [])]
        row["label_ok"] = bool(accepted) and normalise(row["label"]) in accepted
    if kind in ("point", "label+point"):
        point = as_point(answer)
        gold = truth.get("point")
        if point is None or not gold:
            row["distance_px"] = None
            row["point_ok"] = False
        else:
            distance = math.dist(point, (gold["x"], gold["y"]))
            row["distance_px"] = round(distance, 1)
            row["point_ok"] = distance <= tolerance
            row["point_ok_64"] = distance <= 2 * tolerance
        row["gold"] = gold
    if kind == "label+point":
        row["ok"] = bool(row.get("label_ok")) and bool(row.get("point_ok"))
    elif kind == "point":
        row["ok"] = bool(row.get("point_ok"))
    else:
        row["ok"] = bool(row.get("label_ok"))
    return row


# --------------------------------------------------------------------------- #
# Transport
# --------------------------------------------------------------------------- #
def frame_b64(path: str) -> str:
    with open(path, "rb") as fh:
        return base64.b64encode(fh.read()).decode()


def build_completion_prompt(prompt: str, marker: str) -> str:
    """A raw prompt for servers whose chat template cannot place an image.

    moondream2's GGUF carries a vicuna template with no media insertion point, so
    `/v1/chat/completions` answers 400 "Failed to tokenize prompt" for any image
    part. The same build exposes the media marker in `/props`, and
    `/completion` accepts `image_data` plus a prompt that contains that marker.
    Same image, same question, same `temperature: 0`.
    """
    return f"USER: {marker} {prompt} ASSISTANT:"


def media_marker() -> str:
    with urllib.request.urlopen(f"{MODEL_URL}/props", timeout=30) as response:
        props = json.loads(response.read().decode())
    marker = (props.get("media_marker") or "").strip()
    if not marker:
        raise RuntimeError("server reports no media_marker, cannot fall back")
    return marker


def ask_completion(prompt: str, image_b64: str) -> dict:
    """One vision completion through `/completion`, retried on transport failure."""
    payload = {
        "prompt": build_completion_prompt(prompt, media_marker()),
        "image_data": [{"data": image_b64, "id": 0}],
        "n_predict": MAX_TOKENS,
        "temperature": 0,
    }
    data = json.dumps(payload).encode()
    last: Exception | None = None
    for attempt in range(ATTEMPTS):
        request = urllib.request.Request(
            f"{MODEL_URL}/completion",
            data=data,
            headers={"content-type": "application/json"},
        )
        try:
            started = time.perf_counter()
            with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
                body = json.loads(response.read().decode())
            body["wall_ms"] = round((time.perf_counter() - started) * 1000, 1)
            # Shape the reply like a chat completion so the caller has one path.
            body.setdefault("choices", [{"message": {"content": body.get("content", "")}}])
            usage = body.get("usage") or {}
            usage.setdefault("prompt_tokens", body.get("tokens_evaluated"))
            usage.setdefault("completion_tokens", body.get("tokens_predicted"))
            body["usage"] = usage
            return body
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as error:
            last = error
            if attempt + 1 < ATTEMPTS:
                time.sleep(2 * (attempt + 1))
    raise RuntimeError(f"{type(last).__name__}: {last}")


def ask(prompt: str, image_b64: str) -> dict:
    """One vision completion, `temperature: 0`, retried on transport failure."""
    payload = {
        "model": "local",
        "temperature": 0,
        "max_tokens": MAX_TOKENS,
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": prompt},
                    {
                        "type": "image_url",
                        "image_url": {"url": f"data:image/png;base64,{image_b64}"},
                    },
                ],
            }
        ],
    }
    data = json.dumps(payload).encode()
    last: Exception | None = None
    for attempt in range(ATTEMPTS):
        request = urllib.request.Request(
            f"{MODEL_URL}/v1/chat/completions",
            data=data,
            headers={"content-type": "application/json"},
        )
        try:
            started = time.perf_counter()
            with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
                body = json.loads(response.read().decode())
            body["wall_ms"] = round((time.perf_counter() - started) * 1000, 1)
            body["transport"] = "chat"
            return body
        except urllib.error.HTTPError as error:
            # 400 on an image part means the template cannot place the image;
            # that is a property of the model file, not a transport failure.
            if error.code == 400:
                body = ask_completion(prompt, image_b64)
                body["transport"] = "completion"
                return body
            last = error
            if attempt + 1 < ATTEMPTS:
                time.sleep(2 * (attempt + 1))
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as error:
            last = error
            if attempt + 1 < ATTEMPTS:
                time.sleep(2 * (attempt + 1))
    raise RuntimeError(f"{type(last).__name__}: {last}")


def reply_text(body: dict) -> str:
    try:
        content = body["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError):
        return ""
    if isinstance(content, list):
        return " ".join(part.get("text", "") for part in content if isinstance(part, dict))
    return content or ""


# --------------------------------------------------------------------------- #
# Main
# --------------------------------------------------------------------------- #
def main() -> int:
    with open(os.path.join(FIXTURES, "tasks.json"), encoding="utf-8") as fh:
        spec = json.load(fh)
    with open(os.path.join(FIXTURES, "labels.json"), encoding="utf-8") as fh:
        truth = json.load(fh)

    width, height = spec["viewport"]["width"], spec["viewport"]["height"]
    tolerance = float(truth.get("tolerance_px", 32))
    tasks = spec["tasks"]

    print(f"[{ARM}] {MODEL_URL} — {len(tasks)} tasks, tolerance {tolerance} px", flush=True)

    # One warm-up completion, discarded: the first request to a llama-server
    # instance pays the shader-cache and projector warm-up, and that cost belongs
    # to neither task in the table.
    warm_task = tasks[0]
    warm_frame = os.path.join(FRAMES, spec["frames"][warm_task["frame"]])
    try:
        ask(build_prompt(warm_task, width, height), frame_b64(warm_frame))
        print(f"[{ARM}] warm-up done", flush=True)
    except RuntimeError as error:
        print(f"[{ARM}] warm-up failed: {error}", flush=True)

    rows: list[dict] = []
    for task in tasks:
        frame = os.path.join(FRAMES, spec["frames"][task["frame"]])
        row: dict = {
            "arm": ARM,
            "task": task["id"],
            "kind": task["kind"],
            "frame": spec["frames"][task["frame"]],
            "ask": task["ask"],
        }
        try:
            body = ask(build_prompt(task, width, height), frame_b64(frame))
            text = reply_text(body)
            answer = first_json_object(text)
            row.update(score(task, truth["tasks"].get(task["id"], {}), answer, tolerance))
            row["raw"] = text.strip()[:400]
            row["wall_ms"] = body.get("wall_ms")
            usage = body.get("usage") or {}
            row["prompt_tokens"] = usage.get("prompt_tokens")
            row["completion_tokens"] = usage.get("completion_tokens")
            timings = body.get("timings") or {}
            row["prompt_ms"] = timings.get("prompt_ms")
            row["predicted_ms"] = timings.get("predicted_ms")
            row["err"] = ""
        except RuntimeError as error:
            row.update({"parsed": False, "ok": False, "err": str(error)})
        rows.append(row)
        print("ROW " + json.dumps(row), flush=True)

    by_kind: dict[str, dict] = {}
    for row in rows:
        bucket = by_kind.setdefault(row["kind"], {"n": 0, "ok": 0, "invalid_json": 0,
                                                  "distances": [], "wall_ms": []})
        bucket["n"] += 1
        bucket["ok"] += 1 if row.get("ok") else 0
        bucket["invalid_json"] += 0 if row.get("parsed") else 1
        if isinstance(row.get("distance_px"), (int, float)):
            bucket["distances"].append(row["distance_px"])
        if isinstance(row.get("wall_ms"), (int, float)):
            bucket["wall_ms"].append(row["wall_ms"])
    for bucket in by_kind.values():
        distances = bucket.pop("distances")
        walls = bucket.pop("wall_ms")
        bucket["mean_distance_px"] = round(sum(distances) / len(distances), 1) if distances else None
        bucket["median_wall_ms"] = (
            round(sorted(walls)[len(walls) // 2], 1) if walls else None
        )

    result = {
        "arm": ARM,
        "model_url": MODEL_URL,
        "viewport": spec["viewport"],
        "tolerance_px": tolerance,
        "tasks": len(tasks),
        "ok": sum(1 for row in rows if row.get("ok")),
        "invalid_json": sum(1 for row in rows if not row.get("parsed")),
        "errors": sum(1 for row in rows if row.get("err")),
        "by_kind": by_kind,
        "rows": rows,
    }
    print("RESULT_JSON " + json.dumps(result), flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
