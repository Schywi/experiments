"""Minimal OpenAI-compatible TTS API around kokoro-onnx (CPU).

Exposes POST /v1/audio/speech ({input, voice, speed}) -> WAV bytes, plus a
/healthz endpoint for probes. Deliberately tiny: no ffmpeg, WAV only, stdlib
wave writer so the image stays small.
"""

import io
import os
import wave

import numpy as np
from fastapi import FastAPI, Response
from pydantic import BaseModel

from kokoro_onnx import Kokoro

MODEL_PATH = os.environ.get("KOKORO_MODEL", "/models/kokoro-v1.0.onnx")
VOICES_PATH = os.environ.get("KOKORO_VOICES", "/models/voices-v1.0.bin")
DEFAULT_VOICE = os.environ.get("KOKORO_VOICE", "af_sarah")

app = FastAPI(title="kokoro-tts")
kokoro = Kokoro(MODEL_PATH, VOICES_PATH)


class SpeechRequest(BaseModel):
    input: str
    voice: str | None = None
    speed: float = 1.0


def _to_wav(samples: "np.ndarray", sample_rate: int) -> bytes:
    pcm = (np.clip(samples, -1.0, 1.0) * 32767.0).astype("<i2").tobytes()
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(sample_rate)
        wav.writeframes(pcm)
    return buf.getvalue()


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok"}


@app.get("/v1/voices")
def voices() -> dict:
    try:
        return {"voices": sorted(kokoro.get_voices())}
    except Exception:  # pragma: no cover - depends on library version
        return {"voices": []}


@app.post("/v1/audio/speech")
def speech(req: SpeechRequest) -> Response:
    voice = req.voice or DEFAULT_VOICE
    samples, sample_rate = kokoro.create(req.input, voice=voice, speed=req.speed, lang="en-us")
    return Response(content=_to_wav(samples, sample_rate), media_type="audio/wav")
