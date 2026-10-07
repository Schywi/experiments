"""Milestone 10: memory, kept deliberately simple and local.

The charter draws a line between kinds of state; this module implements exactly
two of them, separately:

  * conversation history — short-term, per session, capped, JSONL per session.
  * long-term notes      — durable facts the user explicitly asks to keep.

Both are plain JSONL under MEMORY_DIR (a small PVC). There is no vector
database: notes are few, so recall is deterministic keyword scoring. Secrets do
not belong in either store.
"""

import json
import os
import re
from datetime import datetime, timezone

MEMORY_DIR = os.environ.get("MEMORY_DIR", "/memory")
NOTES_FILE = os.path.join(MEMORY_DIR, "notes.jsonl")
SESSIONS_DIR = os.path.join(MEMORY_DIR, "sessions")
MAX_HISTORY_TURNS = int(os.environ.get("MEMORY_MAX_TURNS", "12"))

_SESSION_RE = re.compile(r"[^a-zA-Z0-9_.-]")
_WORD_RE = re.compile(r"[a-z0-9_]+")


def _norm_session(session: str) -> str:
    return _SESSION_RE.sub("_", (session or "default"))[:64]


def _append(path: str, record: dict) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "a", encoding="utf-8") as fh:
        fh.write(json.dumps(record) + "\n")


def _read(path: str) -> list:
    rows = []
    try:
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if line:
                    try:
                        rows.append(json.loads(line))
                    except ValueError:
                        continue
    except OSError:
        return []
    return rows


# --- long-term notes (durable) ----------------------------------------------

def remember(text: str, tag: str | None = None) -> dict:
    """Store a durable note the user asked the assistant to keep."""
    text = (text or "").strip()
    if not text:
        raise ValueError("nothing to remember")
    record = {"ts": datetime.now(timezone.utc).isoformat(), "text": text, "tag": tag}
    _append(NOTES_FILE, record)
    return {"remembered": text, "tag": tag}


def recall(query: str, k: int = 5) -> dict:
    """Return stored notes ranked by keyword overlap with the query."""
    notes = _read(NOTES_FILE)
    terms = _WORD_RE.findall((query or "").lower())
    if not terms:
        picked = notes[-k:]
    else:
        scored = []
        for note in notes:
            hay = str(note.get("text", "")).lower()
            score = sum(hay.count(t) for t in terms)
            if score:
                scored.append((score, note))
        scored.sort(key=lambda pair: -pair[0])
        picked = [note for _, note in scored[:k]]
    return {"query": query, "count": len(picked), "notes": picked}


# --- conversation history (short-term) --------------------------------------

def append_turn(session: str, role: str, content: str) -> None:
    _append(os.path.join(SESSIONS_DIR, _norm_session(session) + ".jsonl"),
            {"ts": datetime.now(timezone.utc).isoformat(), "role": role,
             "content": content})


def recent(session: str, n: int | None = None) -> list:
    """Return the last `n` turns of a session as {role, content}-only dicts."""
    limit = n or MAX_HISTORY_TURNS
    rows = _read(os.path.join(SESSIONS_DIR, _norm_session(session) + ".jsonl"))
    return [{"role": r.get("role", "user"), "content": r.get("content", "")}
            for r in rows[-limit:]]
