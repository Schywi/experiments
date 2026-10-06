"""Milestone 1: the text-first assistant loop.

    POST /chat {"message": "..."} -> {"reply": "..."}

It forwards one turn to the local LLM's OpenAI-compatible
/v1/chat/completions and returns the text. Deliberately stateless and
tool-free: no Kubernetes API, no history, no voice. Those arrive in later
milestones so the intelligence loop can be debugged on its own.
"""

import os

import httpx
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

LLM_URL = os.environ.get("LLM_URL", "http://llm.models.svc.cluster.local:8000").rstrip("/")
LLM_MODEL = os.environ.get("LLM_MODEL", "qwen2.5-1.5b-instruct")
REQUEST_TIMEOUT = float(os.environ.get("REQUEST_TIMEOUT", "60"))
MAX_TOKENS = int(os.environ.get("MAX_TOKENS", "512"))
SYSTEM_PROMPT = os.environ.get(
    "SYSTEM_PROMPT",
    "You are a concise assistant for a small Kubernetes cluster. Answer briefly and plainly.",
)

app = FastAPI(title="assistant", version="0.1.0")


class ChatRequest(BaseModel):
    message: str


class ChatResponse(BaseModel):
    reply: str


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok"}


@app.post("/chat", response_model=ChatResponse)
async def chat(req: ChatRequest) -> ChatResponse:
    messages = [
        {"role": "system", "content": SYSTEM_PROMPT},
        {"role": "user", "content": req.message},
    ]
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
    reply = data["choices"][0]["message"]["content"].strip()
    return ChatResponse(reply=reply)
