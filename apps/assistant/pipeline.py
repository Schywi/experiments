"""Two-call architecture: plan -> parallel fan-out -> correlate.

Replaces the sequential "one tool per LLM hop" loop (agent.investigate).

  PLAN       choose a batch of read-only tools. A deterministic BUNDLE for known
             intents costs **0 LLM calls**; otherwise ONE LLM call emitting
             {"tools": [{"tool": ..., "args": {...}}]}.
  FAN-OUT    run the batch concurrently (threadpool); validate each tool's args
             against its signature (drops bogus kwargs); cap the evidence per
             fact so the correlate prompt fits the model context.
  CORRELATE  ONE LLM call turning the facts into {"screen_md", "speech"} — the
             rich screen text AND the short spoken line together, so speech is
             never tied to a separate hop.

Same local model for plan and correlate; no new LLM. Read-only tools only.
"""

import concurrent.futures as futures
import inspect
import json
import os
import time

import httpx

import llm_metrics
from k8sutil import audit
from tools import REGISTRY

MAX_FACTS = 6
PER_FACT_CHARS = 600      # evidence kept per fact
EVIDENCE_CHARS = 2600     # total evidence handed to correlate
TOOL_TIMEOUT = 8.0
PLAN_MAX_TOKENS = 160     # a tool-call batch is short
CORRELATE_MAX_TOKENS = 450  # screen_md + speech must fit intact (not truncated)


class _LLMError(Exception):
    """Raised when the local LLM call fails (timeout, upstream error)."""

READ_TOOLS = {name: tool for name, tool in REGISTRY.items() if tool.level <= 1}

# Labeled plan catalog. Each entry: (label, criteria-for-Laya, keywords, batch).
# The label is the unit both planners are scored on (bundle-selection accuracy);
# keywords drive the deterministic fast path, criteria drive the Laya decision.
_PLAN_CATALOG = (
    ("unhealthy",
     "pods down, crashes, restarts, unhealthy or failing workloads",
     ("unhealthy", "wrong", "broken", "down", "failing", "health", "problem"),
     [("get_pods", {}), ("get_events", {}),
      ("query_prometheus", {"query": "sum(rate(hubble_drop_total[5m])) by (reason)"})]),
    ("traffic",
     "network flow, throughput, latency, slow requests",
     ("traffic", "network", "flow", "throughput", "latency", "slow"),
     [("query_prometheus", {"query": "sum(rate(hubble_flows_processed_total[5m])) by (verdict)"}),
      ("get_pods", {})]),
    ("drop",
     "dropped packets, packet loss, DNS failures",
     ("drop", "dropped", "dns"),
     [("query_prometheus", {"query": "sum(rate(hubble_drop_total[5m])) by (reason)"}),
      ("get_events", {})]),
    ("topology",
     "cluster structure: workloads, services, pods, ingress, ownership/selection edges, service map",
     ("topolog", "service map", "how does traffic"),
     [("get_topology", {})]),
    ("snapshot",
     "the ingested Cartography graph (a snapshot that may be hours old), asset relationships",
     ("snapshot", "cartograph", "ingested"),
     [("get_topology_snapshot", {"question": ""})]),
)
_DEFAULT_LABEL = "default"
_DEFAULT_BUNDLE = [("get_pods", {}), ("get_events", {})]
# label -> tool batch, and label -> Laya criteria (including the default label).
_PLANS = {label: batch for label, _crit, _kw, batch in _PLAN_CATALOG}
_PLANS[_DEFAULT_LABEL] = _DEFAULT_BUNDLE
_CRITERIA = {label: crit for label, crit, _kw, _batch in _PLAN_CATALOG}
_CRITERIA[_DEFAULT_LABEL] = "anything else / a general cluster question"

_PLAN_SYS = (
    "You choose read-only tools to answer a question about a Kubernetes cluster. "
    'Reply with ONLY JSON: {"tools": [{"tool": "<name>", "args": {}}]}. '
    "Pick at most 4 independent tools. Available tools:\n"
)
_CORRELATE_SYS = (
    "You are an SRE assistant. Using ONLY the evidence, answer the question. "
    'Reply with ONLY JSON: {"screen_md": "<rich markdown>", '
    '"speech": "<1-2 short conversational sentences, no markdown, no paths>"}. '
    "In screen_md separate OBSERVED FACTS from HYPOTHESES and name the tool each "
    "fact came from. If evidence is insufficient, say so. speech <= 240 chars."
)


def _extract_json(text: str):
    stripped = text.strip()
    if stripped.startswith("```"):          # tolerate ```json ... ``` fences
        stripped = stripped.strip("`").strip()
        if stripped[:4].lower() == "json":
            stripped = stripped[4:].lstrip()
    start, end = stripped.find("{"), stripped.rfind("}")
    if start == -1 or end <= start:
        return None
    try:
        obj = json.loads(stripped[start:end + 1])
    except (ValueError, TypeError):
        return None
    return obj if isinstance(obj, dict) else None


def _looks_like_json(text: str) -> bool:
    """True when text looks like leaked raw JSON rather than a rendered answer."""
    t = (text or "").strip()
    return t.startswith("{") or t.startswith("[") or '"screen_md"' in t


def _parse_tools(obj) -> list:
    if not isinstance(obj, dict):
        return []
    batch = []
    for item in (obj.get("tools") or [])[:MAX_FACTS]:
        if isinstance(item, dict) and item.get("tool"):
            batch.append((str(item["tool"]), item.get("args") or {}))
    return batch


def _parse_answer(raw: str):
    """Return (screen_md, speech) ONLY if the {screen_md, speech} contract held."""
    obj = _extract_json(raw)
    if not isinstance(obj, dict):
        return None, None
    screen = str(obj.get("screen_md") or "").strip()
    if not screen or _looks_like_json(screen):     # reject a leaked/nested JSON blob
        return None, None
    speech = str(obj.get("speech") or "").strip() or screen[:200]
    return screen, speech


def _fallback_digest(facts) -> dict:
    """A clean, deterministic answer when the model cannot produce the contract.

    Never the raw model output — a readable list of the evidence we did gather.
    """
    lines = [f"- [{f['tool']}] {f['evidence'][:180]}" for f in facts] or ["- (no evidence)"]
    return {"screen_md": "Could not format a summary. Raw evidence:\n" + "\n".join(lines),
            "speech": "I gathered the evidence but could not summarize it."}


def _llm(llm_url, model, messages, timeout, stage, max_tokens=512, json_mode=False):
    """One LLM call, recorded by stage. Returns (text, ms). Raises _LLMError.

    With json_mode the request constrains the model to a single JSON object
    (llama.cpp honours OpenAI `response_format`) and adds a mild repeat penalty to
    break the degenerate loops small models fall into. This is the enforcement the
    prompt alone could not provide.
    """
    payload = {"model": model, "messages": messages,
               "max_tokens": max_tokens, "temperature": 0.2}
    if json_mode:
        payload["response_format"] = {"type": "json_object"}
        payload["repeat_penalty"] = 1.1
    t0 = time.perf_counter()
    try:
        with httpx.Client(timeout=timeout) as client:
            resp = client.post(f"{llm_url.rstrip('/')}/v1/chat/completions", json=payload)
            resp.raise_for_status()
            data = resp.json()
    except httpx.HTTPError as exc:
        raise _LLMError(f"{type(exc).__name__}: {exc}") from exc
    ms = (time.perf_counter() - t0) * 1000
    llm_metrics.record_payload(stage, model, data, total_ms=ms)
    return data["choices"][0]["message"]["content"].strip(), ms


# --- PLAN --------------------------------------------------------------------

def _bundle_for(question: str):
    lowered = question.lower()
    for label, _crit, keywords, batch in _PLAN_CATALOG:
        if any(k in lowered for k in keywords):
            return label, batch
    return None


def _catalog() -> str:
    return "\n".join(f"- {n}: {t.description}" for n, t in sorted(READ_TOOLS.items()))


def plan(question, llm_url, model, timeout, history=None, kind=None):
    """Return (batch, source, plan_ms).

    `source` is label-qualified ("bundle:<label>", "laya:<label>") so the chosen
    plan is observable for scoring; "llm"/"llm-failed" is the free-form Qwen path.

    `kind` (default: PLANNER_KIND env, else "qwen") selects the planner. Setting
    it to "laya" routes ONLY this step through the Laya/Jev decision engine; the
    /plan debug endpoint passes it explicitly so both planners can be benchmarked
    from one deployment. Every other stage is unchanged.
    """
    kind = (kind or os.environ.get("PLANNER_KIND", "qwen")).lower()
    if kind == "laya":
        import laya_planner
        try:
            label, ms = laya_planner.classify(question, _CRITERIA)
        except Exception:                        # Laya down -> deterministic fallback
            label, ms = None, 0.0
        if label in _PLANS:
            return _PLANS[label], f"laya:{label}", ms
        bundle = _bundle_for(question)
        if bundle is not None:
            return bundle[1], f"laya-fallback:{bundle[0]}", ms
        return _DEFAULT_BUNDLE, "laya-fallback:default", ms

    bundle = _bundle_for(question)
    if bundle is not None:
        return bundle[1], f"bundle:{bundle[0]}", 0.0
    messages = [{"role": "system", "content": _PLAN_SYS + _catalog()},
                *(history or []),
                {"role": "user", "content": question}]
    ms = 0.0
    for _ in range(2):                              # one plan call + one retry
        try:
            raw, took = _llm(llm_url, model, messages, timeout, "plan",
                             max_tokens=PLAN_MAX_TOKENS, json_mode=True)
            ms += took
        except _LLMError:
            return _DEFAULT_BUNDLE, "llm-failed", ms
        batch = _parse_tools(_extract_json(raw))
        if batch:
            return batch, "llm", ms
    return _DEFAULT_BUNDLE, "llm-fallback", ms


# --- FAN-OUT -----------------------------------------------------------------

def _validate_args(tool, args) -> dict:
    """Keep only kwargs the tool actually accepts (fixes bogus args)."""
    if not isinstance(args, dict):
        return {}
    params = inspect.signature(tool.fn).parameters
    if any(p.kind == p.VAR_KEYWORD for p in params.values()):
        return dict(args)
    return {k: v for k, v in args.items() if k in params}


def _run_one(name: str, args: dict) -> dict:
    tool = READ_TOOLS.get(name)
    if tool is None:
        return {"tool": name, "args": args, "ok": False,
                "evidence": f"unknown tool {name}", "ms": None}
    clean = _validate_args(tool, args)
    audit({"event": "tool_call", "tool": name, "args": clean, "level": tool.level})
    t0 = time.perf_counter()
    try:
        result, ok = json.dumps(tool.fn(**clean)), True
    except Exception as exc:                     # a failed tool is evidence, not a crash
        result, ok = f"{type(exc).__name__}: {exc}", False
    ms = (time.perf_counter() - t0) * 1000
    llm_metrics.record_tool(name, ms)
    return {"tool": name, "args": clean, "ok": ok,
            "evidence": result[:PER_FACT_CHARS], "ms": round(ms, 1)}


def run_batch(batch) -> list:
    """Run a tool batch concurrently; results come back as facts."""
    facts = []
    if not batch:
        return facts
    with futures.ThreadPoolExecutor(max_workers=max(1, len(batch))) as pool:
        submitted = {pool.submit(_run_one, name, args): (name, args)
                     for name, args in batch[:MAX_FACTS]}
        collected = set()
        try:
            for fut in futures.as_completed(submitted, timeout=TOOL_TIMEOUT + 2):
                name, args = submitted[fut]
                collected.add(fut)
                try:
                    facts.append(fut.result())
                except Exception as exc:
                    facts.append({"tool": name, "args": args, "ok": False,
                                  "evidence": f"error: {exc}", "ms": None})
        except futures.TimeoutError:
            for fut, (name, args) in submitted.items():
                if fut not in collected:
                    facts.append({"tool": name, "args": args, "ok": False,
                                  "evidence": "tool timeout", "ms": None})
    return facts


# --- CORRELATE ---------------------------------------------------------------

def correlate(question, facts, llm_url, model, timeout):
    evidence = "\n".join(
        f"[{f['tool']} {json.dumps(f['args'])}] {f['evidence']}" for f in facts
    )[:EVIDENCE_CHARS]
    messages = [
        {"role": "system", "content": _CORRELATE_SYS},
        {"role": "user", "content": f"Question: {question}\n\nEvidence:\n{evidence}"},
    ]
    ms = 0.0
    for _ in range(2):                              # one call + one retry
        try:
            raw, took = _llm(llm_url, model, messages, timeout, "correlate",
                             max_tokens=CORRELATE_MAX_TOKENS, json_mode=True)
            ms += took
        except _LLMError as exc:
            out = _fallback_digest(facts)
            out.update(correlate_ms=round(ms, 1), degraded=str(exc))
            return out
        screen, speech = _parse_answer(raw)
        if screen:
            return {"screen_md": screen, "speech": speech, "correlate_ms": ms}
    # Contract not honoured even with JSON enforcement: degrade to clean evidence.
    out = _fallback_digest(facts)
    out.update(correlate_ms=round(ms, 1), degraded="invalid-json")
    return out


def run(question, llm_url, model, timeout=60.0, history=None, max_rounds=1):
    """plan -> fan-out -> correlate. Two LLM calls (one, with a bundle)."""
    t0 = time.perf_counter()
    batch, source, plan_ms = plan(question, llm_url, model, timeout, history)
    facts = run_batch(batch)
    corr = correlate(question, facts, llm_url, model, timeout)
    metrics = {
        "elapsed_ms": round((time.perf_counter() - t0) * 1000, 1),
        "plan_ms": round(plan_ms, 1),
        "tool_ms": round(sum(f["ms"] or 0 for f in facts), 1),
        "correlate_ms": round(corr["correlate_ms"], 1),
        "rounds": 1, "planner": source, "facts": len(facts),
    }
    return {"screen_md": corr["screen_md"], "speech": corr["speech"],
            "facts": facts, "metrics": metrics, "stopped": "answer"}
