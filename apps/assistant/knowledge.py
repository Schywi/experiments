"""Milestone 6: local knowledge retrieval over a mounted corpus (read-only).

The charter's memory/human-knowledge rule: do not casually reach for a vector
database. The corpus here is small and curated (the repo's own docs), so a
deterministic BM25 ranking over file chunks is enough, needs no extra
dependencies, and adds no model. Embeddings are a deliberate later step, only if
keyword retrieval proves insufficient.

The corpus is a directory (default /corpus) mounted read-only; build it with
scripts/assistant-corpus.sh. An absent/empty corpus returns a note, never an
error.
"""

import math
import os
import re
from collections import Counter
from dataclasses import dataclass

CORPUS_DIR = os.environ.get("CORPUS_DIR", "/corpus")
MAX_FILE_BYTES = int(os.environ.get("CORPUS_MAX_FILE_BYTES", "200000"))
CHUNK_LINES = int(os.environ.get("CORPUS_CHUNK_LINES", "40"))
MAX_CHUNK_CHARS = 1200

_TOKEN_RE = re.compile(r"[a-z0-9_]+")


def _tokens(text: str) -> list:
    return _TOKEN_RE.findall(text.lower())


@dataclass(frozen=True)
class _Chunk:
    source: str
    line: int
    text: str


def _load() -> list:
    chunks = []
    for root, _dirs, files in os.walk(CORPUS_DIR):
        for name in sorted(files):
            path = os.path.join(root, name)
            try:
                if os.path.getsize(path) > MAX_FILE_BYTES:
                    continue
                with open(path, encoding="utf-8", errors="ignore") as fh:
                    lines = fh.read().splitlines()
            except OSError:
                continue
            rel = os.path.relpath(path, CORPUS_DIR)
            for i in range(0, len(lines), CHUNK_LINES):
                text = "\n".join(lines[i:i + CHUNK_LINES])
                if text.strip():
                    chunks.append(_Chunk(rel, i + 1, text))
    return chunks


_CACHE: list | None = None


def _corpus() -> list:
    global _CACHE
    if _CACHE is None:
        _CACHE = _load()
    return _CACHE


def search_knowledge(query: str, k: int = 5) -> dict:
    """Return the top-`k` corpus chunks for a natural-language query (BM25).

    Deterministic and dependency-free; ranks by BM25 over the query terms.
    """
    chunks = _corpus()
    if not chunks:
        return {"query": query, "count": 0, "results": [],
                "note": f"no corpus at {CORPUS_DIR}"}
    docs = [_tokens(c.text) for c in chunks]
    n = len(chunks)
    df: Counter = Counter()
    for d in docs:
        for term in set(d):
            df[term] += 1
    avgdl = sum(len(d) for d in docs) / n or 1.0
    k1, b = 1.5, 0.75
    scored = []
    for chunk, doc in zip(chunks, docs):
        tf = Counter(doc)
        dl = len(doc)
        score = 0.0
        for term in _tokens(query):
            if term not in tf:
                continue
            idf = math.log(1 + (n - df[term] + 0.5) / (df[term] + 0.5))
            score += idf * tf[term] * (k1 + 1) / (tf[term] + k1 * (1 - b + b * dl / avgdl))
        if score > 0:
            scored.append((score, chunk))
    scored.sort(key=lambda pair: -pair[0])
    return {
        "query": query,
        "count": len(scored),
        "results": [
            {"source": c.source, "line": c.line, "score": round(s, 3),
             "text": c.text[:MAX_CHUNK_CHARS]}
            for s, c in scored[:max(1, k)]
        ],
    }
