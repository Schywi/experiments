"""Milestone 7: bounded multi-step investigation loop (read-only).

`tools.route` answers one question with one tool. Here the model chooses tools
step by step: at each step it must reply with a single JSON object, either

    {"tool": "<name>", "args": {...}}   to gather more evidence, or
    {"answer": "<text>"}                when it can answer.

The loop is bounded (AGENT_MAX_STEPS) and only level-1 (read-only) tools are
exposed, so it can look but never change anything. The final answer must separate
observed facts (tool output) from hypotheses (inference).
"""

import json
import os

import httpx

from k8sutil import audit
from tools import REGISTRY

MAX_STEPS = int(os.environ.get("AGENT_MAX_STEPS", "4"))
MAX_TOOL_CHARS = int(os.environ.get("AGENT_MAX_TOOL_CHARS", "4000"))

_LEVEL1 = {name: tool for name, tool in REGISTRY.items() if tool.level <= 1}


def _catalog() -> str:
    return "\n".join(f"- {n}: {t.description}" for n, t in sorted(_LEVEL1.items()))


_SYSTEM = (
    "You are an SRE assistant for a small Kubernetes cluster. Investigate the "
    "question step by step using ONLY the tools listed. Reply with a single JSON "
    "object and nothing else:\n"
    '  {"tool": "<name>", "args": {...}}   to gather evidence, or\n'
    '  {"answer": "<text>"}               when you can answer.\n'
    "Rules: use as few steps as possible; separate OBSERVED FACTS (from tool "
    "output) from HYPOTHESES (your inference); if the evidence is missing, say "
    "so. Never invent an explanation.\n\nAvailable tools:\n"
)


def _ask(llm_url: str, model: str, messages: list, timeout: float) -> str:
    payload = {"model": model, "messages": messages, "max_tokens": 512,
               "temperature": 0.1}
    with httpx.Client(timeout=timeout) as client:
        resp = client.post(f"{llm_url.rstrip('/')}/v1/chat/completions", json=payload)
        resp.raise_for_status()
        return resp.json()["choices"][0]["message"]["content"].strip()


def _extract_json(text: str):
    """Best-effort: return the first {...} object in `text`, else None."""
    start = text.find("{")
    end = text.rfind("}")
    if start == -1 or end <= start:
        return None
    try:
        obj = json.loads(text[start:end + 1])
    except (ValueError, TypeError):
        return None
    return obj if isinstance(obj, dict) else None


def investigate(message: str, llm_url: str, model: str, timeout: float = 60.0,
                history: list | None = None) -> dict:
    """Run the bounded investigation loop and return {reply, steps, stopped}."""
    messages = [
        {"role": "system", "content": _SYSTEM + _catalog()},
        *(history or []),
        {"role": "user", "content": message},
    ]
    steps = []
    for step in range(MAX_STEPS):
        raw = _ask(llm_url, model, messages, timeout)
        obj = _extract_json(raw)
        if obj and "answer" in obj:
            return {"reply": str(obj["answer"]).strip(), "steps": steps, "stopped": "answer"}
        if obj and "tool" in obj:
            name = str(obj["tool"])
            args = obj.get("args") or {}
            tool = _LEVEL1.get(name)
            messages.append({"role": "assistant", "content": raw})
            if tool is None:
                messages.append({"role": "user", "content":
                    f"Unknown tool {name!r}. Use one of: {', '.join(sorted(_LEVEL1))}."})
                continue
            audit({"event": "tool_call", "tool": name, "args": args,
                   "level": tool.level, "step": step})
            try:
                result = tool.fn(**args) if isinstance(args, dict) else {}
            except Exception as exc:  # a failed tool is evidence, not a crash
                result = {"error": f"{type(exc).__name__}: {exc}"}
            steps.append({"tool": name, "args": args})
            messages.append({"role": "user", "content":
                f"Tool result ({name}):\n{json.dumps(result)[:MAX_TOOL_CHARS]}"})
            continue
        # Unparseable reply: nudge once and continue the loop.
        messages.append({"role": "assistant", "content": raw})
        messages.append({"role": "user", "content":
            'Reply with ONLY the JSON object described ({"tool": ...} or {"answer": ...}).'})

    messages.append({"role": "user", "content":
        'Stop investigating. Answer now with {"answer": ...}, separating '
        "observed facts from hypotheses."})
    raw = _ask(llm_url, model, messages, timeout)
    obj = _extract_json(raw) or {}
    return {"reply": str(obj.get("answer") or raw).strip(), "steps": steps,
            "stopped": "max_steps"}
