"""In-memory TTS cache for background Kokoro synthesis.

When a reply's speech text is ready, the server starts synthesis immediately in
the background and stores the WAV here under a short id. The UI then plays
GET /audio/<id> instead of triggering synthesis on click. Single-replica and
in-memory by design (a lab); entries are TTL- and size-bounded.
"""

import time
import uuid

MAX_ENTRIES = 24
TTL_SECONDS = 900

_items: dict = {}


def _evict() -> None:
    now = time.time()
    for key in [k for k, v in _items.items() if now - v["ts"] > TTL_SECONDS]:
        _items.pop(key, None)
    while len(_items) > MAX_ENTRIES:
        oldest = min(_items, key=lambda k: _items[k]["ts"])
        _items.pop(oldest, None)


def new_id() -> str:
    """Reserve a slot, returning its id (audio not yet available)."""
    _evict()
    audio_id = uuid.uuid4().hex
    _items[audio_id] = {"wav": None, "error": None, "ts": time.time()}
    return audio_id


def put(audio_id: str, wav: bytes) -> None:
    if audio_id in _items:
        _items[audio_id].update(wav=wav, error=None, ts=time.time())


def put_error(audio_id: str, error: str) -> None:
    if audio_id in _items:
        _items[audio_id].update(error=error, ts=time.time())


def get(audio_id: str):
    return _items.get(audio_id)
