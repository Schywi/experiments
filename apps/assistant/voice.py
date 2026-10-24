"""Milestone 8: voice, kept decoupled from the intelligence layer.

The core loop is text -> text. Voice is a bolt-on so the intelligence layer can
always be debugged without audio:

    speech -> STT -> [ text -> assistant -> text ] -> TTS -> speech

The assistant itself only needs to expose text plus, here, two thin proxies:
TTS is the in-cluster Kokoro service (apps/models/kokoro); STT is whatever local
service is pointed at by STT_URL (e.g. a whisper.cpp `llama-server`), and is
entirely optional. Nothing about the text loop depends on either.
"""

import os

import httpx

KOKORO_URL = os.environ.get(
    "KOKORO_URL", "http://kokoro.models.svc.cluster.local:8000"
).rstrip("/")
KOKORO_VOICE = os.environ.get("KOKORO_VOICE", "af_sarah")
STT_URL = os.environ.get("STT_URL", "").rstrip("/")
VOICE_TIMEOUT = float(os.environ.get("VOICE_TIMEOUT", "60"))


def speak(text: str, voice: str | None = None) -> bytes:
    """Text -> WAV bytes via Kokoro's OpenAI-compatible /v1/audio/speech."""
    payload = {"input": text, "voice": voice or KOKORO_VOICE}
    with httpx.Client(timeout=VOICE_TIMEOUT) as client:
        resp = client.post(f"{KOKORO_URL}/v1/audio/speech", json=payload)
        resp.raise_for_status()
        return resp.content


def stt_configured() -> bool:
    return bool(STT_URL)


def transcribe(data: bytes, filename: str = "audio.wav") -> str:
    """Speech (WAV/other) -> text via a local OpenAI-compatible STT service."""
    if not STT_URL:
        raise RuntimeError("STT_URL is not configured")
    files = {"file": (filename, data, "audio/wav")}
    with httpx.Client(timeout=VOICE_TIMEOUT) as client:
        resp = client.post(f"{STT_URL}/v1/audio/transcriptions", files=files)
        resp.raise_for_status()
        return str(resp.json().get("text", "")).strip()
