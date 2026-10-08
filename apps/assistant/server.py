"""Milestone 2: the assistant loop with one read-only infrastructure tool.

    POST /chat {"message": "..."} -> {"reply": "..."}

Flow: message -> route() -> (optional) tool call as the Pod's ServiceAccount ->
result fed back to the local LLM to phrase -> reply. Every tool call is audited
to JSONL. Only level-1 (read-only) tools exist so far; level >= 2 will require
human confirmation in a later milestone.
"""

import asyncio
import json
import os
import time

import httpx
from fastapi import FastAPI, File, Form, HTTPException, Response, UploadFile
from fastapi.responses import HTMLResponse
from pydantic import BaseModel

import ui  # M11
import voice  # M8
import actions  # M9
import memory  # M10
import llm_metrics  # instrumentation
import pipeline  # plan -> fan-out -> correlate
import tts_cache  # background TTS
from tools import REGISTRY, audit, route
from starlette.concurrency import run_in_threadpool

LLM_URL = os.environ.get("LLM_URL", "http://llm.models.svc.cluster.local:8000").rstrip("/")
LLM_MODEL = os.environ.get("LLM_MODEL", "qwen2.5-1.5b-instruct")
REQUEST_TIMEOUT = float(os.environ.get("REQUEST_TIMEOUT", "60"))
# The two-call pipeline can take longer than a single chat call (plan + fan-out +
# correlate); give it headroom before the correlate step degrades.
PIPELINE_TIMEOUT = float(os.environ.get("PIPELINE_TIMEOUT", "150"))
# Background TTS: synthesize speech as soon as it exists, cache the WAV.
TTS_BACKGROUND = os.environ.get("TTS_BACKGROUND", "true").lower() == "true"
MAX_TOKENS = int(os.environ.get("MAX_TOKENS", "512"))
# M11: resource links shown in the chat UI header (JSON list of {label,url}).
UI_LINKS = json.loads(os.environ.get("UI_LINKS", "[]") or "[]")
SYSTEM_PROMPT = os.environ.get(
    "SYSTEM_PROMPT",
    "You are a concise assistant for a small Kubernetes cluster. "
    "Answer briefly and plainly, using only the data given to you.",
)

app = FastAPI(title="assistant", version="0.2.0")


class ChatRequest(BaseModel):
    message: str
    # Optional conversation id: when set, recent turns are included and the new
    # turn is stored (M10). Stateless when omitted.
    session: str | None = None


class ChatResponse(BaseModel):
    reply: str
    tool: str | None = None
    metrics: dict | None = None
    # Two-call pipeline outputs (screen text for the eye, speech text for TTS).
    screen_md: str | None = None
    speech: str | None = None
    facts: list | None = None
    # Background TTS: id to fetch the pre-synthesized WAV from /audio/<id>.
    audio_id: str | None = None


def _start_tts(text: str | None) -> str | None:
    """Kick off speech synthesis in the background; return its audio id.

    Returns None if disabled or the text is empty. The task outlives the request.
    """
    text = (text or "").strip()
    if not TTS_BACKGROUND or not text:
        return None
    audio_id = tts_cache.new_id()

    async def _job():
        try:
            wav = await run_in_threadpool(voice.speak, text)
            tts_cache.put(audio_id, wav)
        except Exception as exc:                     # noqa: BLE001
            tts_cache.put_error(audio_id, f"{type(exc).__name__}: {exc}")

    asyncio.create_task(_job())
    return audio_id


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


async def _ask_llm_stream(messages: list, stage: str = "chat") -> tuple:
    """Stream a reply to measure time-to-first-token; returns (text, ttft_ms, llm_ms)."""
    payload = {"model": LLM_MODEL, "messages": messages,
               "max_tokens": MAX_TOKENS, "temperature": 0.2, "stream": True}
    parts, first, t0 = [], None, time.perf_counter()
    usage: dict = {}
    timings: dict = {}
    try:
        async with httpx.AsyncClient(timeout=REQUEST_TIMEOUT) as client:
            async with client.stream("POST", f"{LLM_URL}/v1/chat/completions",
                                     json=payload) as resp:
                resp.raise_for_status()
                async for line in resp.aiter_lines():
                    if not line.startswith("data:"):
                        continue
                    data = line[5:].strip()
                    if data == "[DONE]":
                        break
                    try:
                        obj = json.loads(data)
                    except ValueError:
                        continue
                    if obj.get("usage"):
                        usage = obj["usage"]
                    if obj.get("timings"):
                        timings = obj["timings"]
                    delta = (obj.get("choices") or [{}])[0].get("delta", {}).get("content")
                    if delta:
                        if first is None:
                            first = time.perf_counter()
                        parts.append(delta)
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"LLM upstream error: {exc}") from exc
    total = time.perf_counter() - t0
    ttft = None if first is None else (first - t0) * 1000
    llm_metrics.record_payload(stage, LLM_MODEL, {"usage": usage, "timings": timings},
                               total_ms=total * 1000, ttft_ms=ttft)
    return "".join(parts).strip(), ttft, total * 1000


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok"}


@app.post("/chat", response_model=ChatResponse)
async def chat(req: ChatRequest) -> ChatResponse:
    t0 = time.perf_counter()
    messages, used_tool = _build_messages(req.message)
    if req.session:
        messages = [messages[0], *memory.recent(req.session), *messages[1:]]
    reply, ttft_ms, llm_ms = await _ask_llm_stream(messages)
    metrics = {"elapsed_ms": round((time.perf_counter() - t0) * 1000, 1),
               "llm_ms": round(llm_ms, 1),
               "ttft_ms": None if ttft_ms is None else round(ttft_ms, 1)}
    if req.session:
        memory.append_turn(req.session, "user", req.message)
        memory.append_turn(req.session, "assistant", reply)
    return ChatResponse(reply=reply, tool=used_tool, metrics=metrics,
                        screen_md=reply, speech=reply, audio_id=_start_tts(reply))


@app.post("/investigate", response_model=ChatResponse)
async def investigate_route(req: ChatRequest) -> ChatResponse:
    """Two-call pipeline: plan -> parallel fan-out -> correlate (screen_md + speech)."""
    history = memory.recent(req.session) if req.session else None
    out = await run_in_threadpool(
        pipeline.run, req.message, LLM_URL, LLM_MODEL, PIPELINE_TIMEOUT, history
    )
    used = ",".join(sorted({f["tool"] for f in out["facts"]})) or None
    if req.session:
        memory.append_turn(req.session, "user", req.message)
        memory.append_turn(req.session, "assistant", out["screen_md"])
    return ChatResponse(reply=out["screen_md"], tool=used, metrics=out["metrics"],
                        screen_md=out["screen_md"], speech=out["speech"],
                        facts=out["facts"], audio_id=_start_tts(out["speech"]))


class VoiceRequest(BaseModel):
    message: str
    voice: str | None = None


@app.post("/speak")
async def speak_route(req: VoiceRequest) -> Response:
    """Milestone 8: text -> WAV via the in-cluster Kokoro TTS (voice is a bolt-on)."""
    try:
        audio = await run_in_threadpool(voice.speak, req.message, req.voice)
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"TTS upstream error: {exc}") from exc
    return Response(content=audio, media_type="audio/wav")


@app.post("/voice/transcribe")
async def transcribe_route(file: UploadFile = File(...)) -> dict:
    """Milestone 8: audio -> text via a local STT service (only if STT_URL is set)."""
    if not voice.stt_configured():
        raise HTTPException(status_code=501, detail="STT not configured (set STT_URL)")
    data = await file.read()
    try:
        text = await run_in_threadpool(
            voice.transcribe, data, file.filename or "audio.wav"
        )
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=502, detail=f"STT upstream error: {exc}") from exc
    return {"text": text}


# --- M9: controlled actions behind human confirmation -----------------------

class ActionRequest(BaseModel):
    tool: str
    args: dict = {}


class ActionConfirm(BaseModel):
    token: str


@app.get("/actions")
def actions_route() -> dict:
    return {"enabled": actions.enabled(), "catalog": actions.catalog(),
            "pending": actions.list_pending()}


@app.post("/actions/request")
def action_request_route(req: ActionRequest) -> dict:
    """Mint a confirmation token for a level-2 action. Nothing is executed."""
    try:
        return actions.request(req.tool, req.args)
    except actions.PolicyError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.post("/actions/confirm")
def action_confirm_route(req: ActionConfirm) -> dict:
    """Execute a previously requested action. Single-use token; human-triggered."""
    try:
        return actions.confirm(req.token)
    except actions.UnknownToken as exc:
        raise HTTPException(status_code=404, detail="unknown or already-used token") from exc
    except actions.Expired as exc:
        raise HTTPException(status_code=410, detail="token expired") from exc
    except actions.PolicyError as exc:
        raise HTTPException(status_code=403, detail=str(exc)) from exc


# --- M11: htmx chat front-end ----------------------------------------------

@app.get("/audio/{audio_id}")
def audio(audio_id: str) -> Response:
    """Serve a background-synthesized WAV; 202 while still generating, 404 unknown."""
    item = tts_cache.get(audio_id)
    if item is None:
        raise HTTPException(status_code=404, detail="unknown audio id")
    if item["error"]:
        raise HTTPException(status_code=502, detail=f"TTS failed: {item['error']}")
    if item["wav"] is None:
        return Response(status_code=202, headers={"Retry-After": "1"})
    return Response(content=item["wav"], media_type="audio/wav",
                    headers={"Cache-Control": "private, max-age=600"})


@app.get("/metrics")
def metrics_endpoint() -> Response:
    """Prometheus exposition (LLM per-stage instrumentation; measurement only)."""
    return Response(content=llm_metrics.payload(), media_type=llm_metrics.CONTENT_TYPE)


@app.get("/", response_class=HTMLResponse)
def index() -> HTMLResponse:
    """The whole UI: one page, same origin as the JSON API."""
    return HTMLResponse(ui.index(UI_LINKS))


@app.post("/ui/message", response_class=HTMLResponse)
async def ui_message(
    message: str = Form(...),
    mode: str = Form("investigate"),
    session: str = Form(""),
) -> HTMLResponse:
    """htmx target: answer one turn and return the HTML fragment to append."""
    msg = message.strip()
    if not msg:
        return HTMLResponse("")
    t0 = time.perf_counter()
    speech = None
    try:
        if mode == "chat":
            messages, tool = _build_messages(msg)
            if session:
                messages = [messages[0], *memory.recent(session), *messages[1:]]
            reply, ttft_ms, llm_ms = await _ask_llm_stream(messages)
            speech = reply
            metrics = {"elapsed_ms": round((time.perf_counter() - t0) * 1000, 1),
                       "llm_ms": round(llm_ms, 1),
                       "ttft_ms": None if ttft_ms is None else round(ttft_ms, 1)}
        else:
            history = memory.recent(session) if session else None
            out = await run_in_threadpool(
                pipeline.run, msg, LLM_URL, LLM_MODEL, PIPELINE_TIMEOUT, history
            )
            reply, speech = out["screen_md"], out["speech"]
            tool = ",".join(sorted({f["tool"] for f in out["facts"]})) or None
            metrics = out["metrics"]
    except HTTPException as exc:
        return HTMLResponse(ui.assistant_bubble(str(exc.detail), error=True))
    if session:
        memory.append_turn(session, "user", msg)
        memory.append_turn(session, "assistant", reply)
    audio_id = _start_tts(speech)
    return HTMLResponse(ui.assistant_bubble(reply, speech=speech, tool=tool,
                                            metrics=metrics, audio_id=audio_id,
                                            markdown=(mode != "chat")))
