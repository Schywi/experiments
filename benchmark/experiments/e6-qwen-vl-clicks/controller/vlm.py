"""One client for the Qwen3-VL server, shared by the calibration and the controller.

Every model call in E6 goes through `ask()`, so the prompt shape, the sampling
parameters and the reply parsing cannot drift between the call that fits the
coordinate transform and the calls that get scored against it.

Stdlib only: the image that runs this is python:3.12-slim and has no index.
"""

from __future__ import annotations

import base64
import json
import os
import time
import urllib.error
import urllib.request

MODEL_URL = os.environ.get("MODEL_URL", "http://127.0.0.1:8000").rstrip("/")
MAX_TOKENS = int(os.environ.get("MAX_TOKENS", "320"))
TIMEOUT = float(os.environ.get("TIMEOUT", "600"))
ATTEMPTS = int(os.environ.get("ATTEMPTS", "3"))

POINT_INSTRUCTION = (
    "Answer with exactly one JSON object and nothing else, no prose: "
    '{"label": "<short name of what you found>", "x": <integer>, "y": <integer>}. '
    "x and y are pixel coordinates measured from the top-left corner of the image."
)


def image_b64(path: str) -> str:
    with open(path, "rb") as fh:
        return base64.b64encode(fh.read()).decode()


def first_json_object(text: str) -> dict | None:
    """The first balanced {...} in a reply, or None.

    A reply that only mentions a coordinate is not an answer, and a reply with two
    objects is one answer followed by noise; scanning for the first balanced object
    handles both without guessing.
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


def ask(prompt: str, image: str, *, max_tokens: int | None = None) -> dict:
    """One vision completion at temperature 0.

    Returns the parsed reply plus the bookkeeping the events need:
    `{ok, raw, answer, wall_ms, prompt_tokens, completion_tokens, transport, error}`.
    `answer` is the parsed JSON object or None; `error` is set when the transport
    failed, never when the model answered badly — a well-formed wrong answer is a
    measurement, not an error.
    """
    payload = {
        "model": "local",
        "temperature": 0,
        "max_tokens": max_tokens or MAX_TOKENS,
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": prompt},
                    {"type": "image_url", "image_url": {"url": f"data:image/png;base64,{image}"}},
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
            wall_ms = round((time.perf_counter() - started) * 1000, 1)
            raw = _reply_text(body)
            usage = body.get("usage") or {}
            return {
                "ok": True,
                "raw": raw,
                "answer": first_json_object(raw),
                "wall_ms": wall_ms,
                "prompt_tokens": usage.get("prompt_tokens"),
                "completion_tokens": usage.get("completion_tokens"),
                "transport": "chat",
                "error": "",
            }
        except urllib.error.HTTPError as error:
            detail = ""
            try:
                detail = error.read().decode(errors="replace")[:300]
            except Exception:  # noqa: BLE001 - reading an error body must not raise
                pass
            last = RuntimeError(f"HTTP {error.code}: {detail}")
            if error.code == 400:
                # Some GGUF chat templates cannot place an image part at all
                # (E5: moondream's vicuna template). The controller does not
                # retry those through /completion: for Qwen3-VL, a 400 is a real
                # failure and is recorded as one.
                break
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as error:
            last = error
        if attempt + 1 < ATTEMPTS:
            time.sleep(2 * (attempt + 1))
    return {
        "ok": False,
        "raw": "",
        "answer": None,
        "wall_ms": None,
        "prompt_tokens": None,
        "completion_tokens": None,
        "transport": "chat",
        "error": f"{type(last).__name__}: {last}",
    }


def _reply_text(body: dict) -> str:
    try:
        content = body["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError):
        return ""
    if isinstance(content, list):
        return " ".join(part.get("text", "") for part in content if isinstance(part, dict))
    return content or ""
