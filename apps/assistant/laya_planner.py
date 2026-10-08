"""Laya/Jev closed-set planner — the isolated `assistant-laya` experiment.

ADDITIVE: this module is imported by `pipeline.plan()` **only** when the
`PLANNER_KIND=laya` env is set. The production (`qwen`) path never imports it, so
correlation, tool execution, and the Qwen model are untouched.

Laya serves a *typed decision* API (`POST /v1/systemone`): you hand it the
question as `state.body` and a **closed set** of `criteria` (label -> meaning),
and it returns exactly one label. We use that to choose a canned read-only tool
batch ("bundle"). Laya never authors tool arguments — the plan step stays a
closed-set decision, which is the only shape Laya's API supports.
"""

import os
import time

import httpx

import llm_metrics

LAYA_URL = os.environ.get(
    "LAYA_URL", "http://laya.models.svc.cluster.local:8000"
).rstrip("/")
LAYA_TIMEOUT = float(os.environ.get("LAYA_TIMEOUT", "10"))
_QUESTION = os.environ.get("LAYA_PLAN_QUESTION", "plan")


class LayaError(Exception):
    """Raised when the Laya decision call fails (timeout, upstream, bad shape)."""


def _extract_label(data, question: str):
    """Pull the chosen label out of Laya's response.

    Real shape (verified live):
        {"answers": {"<question>": {"type":"choice", "choice":"traffic",
                                    "probabilities": {...}}}}
    Fall back to the highest-probability option, then a few generic keys.
    """
    if not isinstance(data, dict):
        return None
    answers = data.get("answers")
    if isinstance(answers, dict):
        picked = answers.get(question)
        if isinstance(picked, dict):
            for key in ("choice", "label", "answer"):
                value = picked.get(key)
                if isinstance(value, str):
                    return value
            probs = picked.get("probabilities")
            if isinstance(probs, dict) and probs:
                return max(probs, key=probs.get)
        if isinstance(picked, str):
            return picked
    for key in (question, "choice", "label", "result", "systemone", "decision", "output"):
        value = data.get(key)
        if isinstance(value, str):
            return value
        if isinstance(value, dict):
            for inner in value.values():
                if isinstance(inner, str):
                    return inner
    return None


def classify(question: str, criteria: dict) -> tuple:
    """Ask Laya to pick one label from `criteria`. Returns (label, ms).

    `criteria` maps label -> human description. Raises LayaError on failure so
    the caller can fall back deterministically.
    """
    payload = {
        "state": {"body": question},
        "questions": {
            _QUESTION: {
                "type": "choice",
                "instructions": (
                    "Choose the single read-only investigation that best fits the "
                    "question. Reply with exactly one of the provided labels."
                ),
                "criteria": criteria,
            }
        },
    }
    t0 = time.perf_counter()
    try:
        with httpx.Client(timeout=LAYA_TIMEOUT) as client:
            resp = client.post(f"{LAYA_URL}/v1/systemone", json=payload)
            resp.raise_for_status()
            data = resp.json()
    except httpx.HTTPError as exc:
        raise LayaError(f"{type(exc).__name__}: {exc}") from exc
    ms = (time.perf_counter() - t0) * 1000
    label = _extract_label(data, _QUESTION)
    llm_metrics.record_planner(label or "?", ms, planner="laya")
    return label, ms
