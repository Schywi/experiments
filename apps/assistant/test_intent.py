"""Evaluator tests: the decision it returns from a model reply. No LLM, no cluster.

    python3 apps/assistant/test_intent.py
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import intent  # noqa: E402


def _ask(reply):
    def ask(messages, max_tokens):
        assert "Available tools" in messages[0]["content"]
        return reply
    return ask


def test_two_asks_become_two_tasks():
    reply = json.dumps({"tasks": [
        {"tool": "query_prometheus", "args": {"query": "sum(rate(hubble_drop_total[5m]))"},
         "intent": "prometheus", "why": "dropped packets"},
        {"tool": "sandbox_shot", "args": {"sandbox_id": "browser-pool-72vfg"},
         "intent": "sandbox", "why": "screenshot"}],
        "recipe": None, "recipe_args": {}, "uncovered": []})
    decision = intent.evaluate("dropped packets in worm-lab and a screenshot", _ask(reply))
    assert [t["tool"] for t in decision["tasks"]] == ["query_prometheus", "sandbox_shot"]
    assert [t["intent"] for t in decision["tasks"]] == ["prometheus", "sandbox"]
    assert decision["recipe"] is None


def test_recipe_is_parsed_with_its_args():
    reply = json.dumps({"tasks": [], "recipe": "observe_url",
                        "recipe_args": {"url": "google.com"}, "uncovered": []})
    decision = intent.evaluate("make a sandbox on google.com and screenshot it", _ask(reply))
    assert decision["recipe"] == "observe_url"
    assert decision["recipe_args"]["url"] == "google.com"


def test_unknown_tool_and_unknown_recipe_are_dropped():
    reply = json.dumps({"tasks": [{"tool": "run_shell", "args": {"cmd": "ls"}},
                                  {"tool": "get_pods", "args": {"namespace": "models"}}],
                        "recipe": "rm_rf", "uncovered": ["argo sync status"]})
    decision = intent.evaluate("x", _ask(reply))
    assert [t["tool"] for t in decision["tasks"]] == ["get_pods"]
    assert decision["recipe"] is None
    assert decision["uncovered"] == ["argo sync status"]


def test_tasks_are_capped():
    tasks = [{"tool": "get_pods", "args": {}} for _ in range(9)]
    reply = json.dumps({"tasks": tasks, "recipe": None})
    assert len(intent.evaluate("x", _ask(reply))["tasks"]) == intent.MAX_TASKS


def test_non_json_reply_yields_empty_decision():
    decision = intent.evaluate("x", _ask("I think you should look at the pods."))
    assert decision == {"tasks": [], "recipe": None, "recipe_args": {}, "uncovered": []}


def test_model_failure_yields_empty_decision():
    def boom(messages, max_tokens):
        raise RuntimeError("llm down")
    assert intent.evaluate("x", boom)["tasks"] == []


def test_missing_metric_hint_does_not_break_the_prompt():
    seen = {}

    def ask(messages, max_tokens):
        seen["sys"] = messages[0]["content"]
        return json.dumps({"tasks": [], "recipe": None})
    intent.evaluate("x", ask, ())
    assert "Available tools" in seen["sys"]


if __name__ == "__main__":
    failures = 0
    names = sorted(k for k in list(globals()) if k.startswith("test_"))
    for name in names:
        try:
            globals()[name]()
            print("ok  ", name)
        except Exception as exc:                       # noqa: BLE001
            failures += 1
            print("FAIL", name, "->", exc)
    print(f"\n{len(names) - failures} passed, {failures} failed")
    sys.exit(1 if failures else 0)
