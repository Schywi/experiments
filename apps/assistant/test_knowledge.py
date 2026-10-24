"""Unit tests for M6 knowledge retrieval (no cluster, no LLM needed)."""

import os

import knowledge as kb


def test_search_ranks_the_relevant_file(tmp_path, monkeypatch):
    (tmp_path / "edge.md").write_text(
        "# Edge\nTraffic reaches the cluster through Cilium Ingress.\n"
    )
    (tmp_path / "db.md").write_text(
        "# Database\nPostgres stores rows in tables.\n"
    )
    monkeypatch.setattr(kb, "_CACHE", None)
    monkeypatch.setattr(kb, "CORPUS_DIR", str(tmp_path))

    out = kb.search_knowledge("how does traffic reach the cluster", k=3)
    assert out["count"] >= 1
    assert out["results"][0]["source"] == "edge.md"
    assert "Cilium" in out["results"][0]["text"]


def test_search_empty_corpus_is_a_note(tmp_path, monkeypatch):
    monkeypatch.setattr(kb, "_CACHE", None)
    monkeypatch.setattr(kb, "CORPUS_DIR", str(tmp_path))
    out = kb.search_knowledge("anything")
    assert out["count"] == 0
    assert "no corpus" in out["note"]
