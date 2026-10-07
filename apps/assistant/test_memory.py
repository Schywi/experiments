"""Unit tests for M10 memory (pure stdlib; no cluster, no LLM)."""

import memory


def _fresh(tmp_path, monkeypatch):
    monkeypatch.setattr(memory, "MEMORY_DIR", str(tmp_path))
    monkeypatch.setattr(memory, "NOTES_FILE", str(tmp_path / "notes.jsonl"))
    monkeypatch.setattr(memory, "SESSIONS_DIR", str(tmp_path / "sessions"))


def test_remember_and_recall(tmp_path, monkeypatch):
    _fresh(tmp_path, monkeypatch)
    memory.remember("the cluster runs Cilium", tag="net")
    memory.remember("grafana is anonymous admin", tag="obs")

    out = memory.recall("cilium", k=3)
    assert out["count"] == 1
    assert "Cilium" in out["notes"][0]["text"]


def test_recall_empty_query_returns_latest(tmp_path, monkeypatch):
    _fresh(tmp_path, monkeypatch)
    memory.remember("first")
    memory.remember("second")
    out = memory.recall("", k=1)
    assert out["notes"][0]["text"] == "second"


def test_session_history_is_capped_and_ordered(tmp_path, monkeypatch):
    _fresh(tmp_path, monkeypatch)
    for i in range(20):
        memory.append_turn("s1", "user", f"m{i}")
    recent = memory.recent("s1", n=5)
    assert len(recent) == 5
    assert recent[-1]["content"] == "m19"
    assert set(recent[0]) == {"role", "content"}
