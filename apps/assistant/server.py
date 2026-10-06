"""Milestone 2: the assistant loop with one read-only infrastructure tool.

    POST /chat {"message": "..."} -> {"reply": "..."}

Flow: message -> route() -> (optional) tool call as the Pod's ServiceAccount ->
result fed back to the local LLM to phrase -> reply. Every tool call is audited
to JSONL. Only level-1 (read-only) tools exist so far; level >= 2 will require
human confirmation in a later milestone.
"""

import json
import os

import httpx
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from tools import REGISTRY, audit, route
from agent import investigate  # M7
from starlette.concurrency import run_in_threadpool

LLM_URL = os.environ.get("LLM_URL", "http://llm.models.svc.cluster.local:8000").rstrip("/")
LLM_MODEL = os.environ.get("LLM_MODEL", "qwen2.5-1.5b-instruct")
REQUEST_TIMEOUT = float(os.environ.get("REQUEST_TIMEOUT", "60"))
MAX_TOKENS = int(os.environ.get("MAX_TOKENS", "512"))
SYSTEM_PROMPT = os.environ.get(
    "SYSTEM_PROMPT",
    "You are a concise assistant for a small Kubernetes cluster. "
    "Answer briefly and plainly, using only the data given to you.",
)

app = FastAPI(title="assistant", version="0.2.0")


class ChatRequest(BaseModel):
    message: str


class ChatResponse(BaseModel):
    reply: str
    tool: str | None = None


def _build_messages(message: str) -> tuple[list, str | None]:
    intent = route(message)
    if intent is None:
        return [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": message},
        ], None

    name, args = intent
    tool = REGISTRY[name]
    audit({"event": "tool_call", "tool": name, "args": args, "level": tool.level})
    try:
        result = tool.fn(**args)
    except Exception as exc:  # tool errors surface, never crash the loop
        audit({"event": "tool_error", "tool": name, "error": f"{type(exc).__name__}: {exc}"})
        raise HTTPException(status_code=502, detail=f"tool {name} failed: {exc}") from exc
    audit({"event": "tool_result", "tool": name,
           "count": len(result) if isinstance(result, list) else None})
    data = json.dumps(result, indent=None)[:4000]
    return [
        {"role": "system", "content": SYSTEM_PROMPT},
        {"role": "user", "content":
            f"Question: {message}\n\n"
            f"Live cluster data ({name}):\n{data}\n\n"
            "Answer using only this data. If it is empty, say so."},
    ], name


async def _ask_llm(messages: list) -> str:
    payload = {
        "model": LLM_MODEL,
        "messages": messages,
        "max_tokens": MAX_TOKENS,
        "temperature": 0.2,
    }
    try:
        async with httpx.AsyncClient(timeout=REQUEST_TIMEOUT) as client:
            resp = await client.post(f"{LLM_URL}/v1/chat/completions", json=payload)
            resp.raise_for_status()
            data = resp.json()
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"LLM upstream error: {exc}") from exc
    return data["choices"][0]["message"]["content"].strip()


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok"}


@app.post("/chat", response_model=ChatResponse)
async def chat(req: ChatRequest) -> ChatResponse:
    messages, used_tool = _build_messages(req.message)
    reply = await _ask_llm(messages)
    return ChatResponse(reply=reply, tool=used_tool)


@app.post("/investigate", response_model=ChatResponse)
async def investigate_route(req: ChatRequest) -> ChatResponse:
    """Milestone 7: bounded multi-step investigation over the level-1 tools."""
    out = await run_in_threadpool(
        investigate, req.message, LLM_URL, LLM_MODEL, REQUEST_TIMEOUT
    )
    used = ",".join(step["tool"] for step in out["steps"]) or None
    return ChatResponse(reply=out["reply"], tool=used)
