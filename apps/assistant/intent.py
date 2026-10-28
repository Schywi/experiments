"""Evaluator: read the whole question and say what is being asked.

One JSON-mode LLM call. It decides *which* tools to call and with what intent;
it never decides how a tool behaves — the tools keep their schemas and guards.

Output shape:

    {"tasks": [{"tool", "args", "intent", "why"}],
     "recipe": "observe_url" | "observe_cilium" | null,
     "recipe_args": {"url"?: str, "namespace"?: str},
     "uncovered": [str]}

A flat task list is for independent asks. A recipe is for one ordered chain with
a data handoff (metrics url -> sandbox -> screenshot). See
research/features/assistant/{evaluator,recipes}.md.
"""

import json

import tool_schema

MAX_TASKS = 5
INTENTS = ("prometheus", "kube", "cartography", "sandbox", "general")
RECIPES = ("observe_url", "observe_cilium")

_SYS = (
    "You read ONE request about a Kubernetes cluster and answer what is being asked. "
    "Reply with ONLY a JSON object with the keys tasks, recipe, recipe_args, uncovered. "
    "tasks is an array of objects, each with the keys tool, args, intent, why. "
    "intent is one of: " + ", ".join(INTENTS) + ". "
    "recipe is null, or one of: " + ", ".join(RECIPES) + ". "
    "Use a recipe when the request is ONE ordered chain that hands a value forward: "
    "check metrics (or a URL) then create a sandbox on that URL then take a screenshot. "
    "Use tasks for independent asks; one task per distinct thing asked, so a sentence "
    "with two asks returns two tasks. "
    "Cover the whole request. Put anything no tool can answer into uncovered instead of "
    "guessing. Never invent a sandbox id, a namespace or a metric name: omit the argument "
    "when the request does not contain it. At most " + str(MAX_TASKS) + " tasks. "
    "A bare host such as google.com is a valid url argument; do not add a scheme. "
    "Pick the tool whose description matches what is being asked. Questions about drops, "
    "flows, traffic, latency, DNS or request rates are answered by query_prometheus. A "
    "screenshot of a sandbox that is already named is sandbox_shot. "
    "Example: 'any dropped packets in worm-lab, and a screenshot of browser-pool-x' returns "
    "tasks query_prometheus and sandbox_shot. "
    "Example: 'create a sandbox with google.com and take a screenshot' returns recipe "
    "observe_url with recipe_args {url: google.com} and no tasks, because the screenshot "
    "needs the sandbox that is created first. "
    "Available tools (name(arguments): description):\n"
)


def _catalog() -> str:
    try:
        from tools import READ_TOOLS          # descriptions live with the tools
    except Exception:                        # tooling absent (unit tests): schema only
        READ_TOOLS = {}
    lines = []
    for name in sorted(tool_schema.SCHEMAS):
        spec = tool_schema.SCHEMAS[name] or {}
        args = ", ".join(f"{key}{'*' if req else ''}" for key, (_t, req) in spec.items())
        tool = READ_TOOLS.get(name)
        described = getattr(tool, "description", "") if tool else ""
        lines.append(f"- {name}({args or 'no arguments'}): {described}")
    return "\n".join(lines)


def _clean(decision) -> dict:
    """Keep only tasks naming a real tool with an object args value."""
    if not isinstance(decision, dict):
        return {"tasks": [], "recipe": None, "recipe_args": {}, "uncovered": []}
    tasks = []
    for raw in decision.get("tasks") or []:
        if not isinstance(raw, dict):
            continue
        tool = raw.get("tool")
        if tool not in tool_schema.SCHEMAS:
            continue
        intent = raw.get("intent") if raw.get("intent") in INTENTS else "general"
        args = raw.get("args")
        tasks.append({"tool": tool, "args": args if isinstance(args, dict) else {},
                      "intent": intent, "why": str(raw.get("why") or "")[:120]})
        if len(tasks) >= MAX_TASKS:
            break
    recipe = decision.get("recipe")
    if recipe not in RECIPES:
        recipe = None
    recipe_args = decision.get("recipe_args")
    uncovered = [str(u)[:120] for u in (decision.get("uncovered") or []) if u]
    return {"tasks": tasks, "recipe": recipe,
            "recipe_args": recipe_args if isinstance(recipe_args, dict) else {},
            "uncovered": uncovered[:MAX_TASKS]}


def evaluate(question: str, ask, metric_names=()) -> dict:
    """`ask(messages, max_tokens) -> raw string`. Returns the cleaned decision."""
    system = _SYS + _catalog()
    if metric_names:
        system += "\nMetric names that exist (use only these when writing PromQL):\n" \
                  + ", ".join(metric_names)
    messages = [{"role": "system", "content": system},
                {"role": "user", "content": question}]
    try:
        raw = ask(messages, 320)
    except Exception:                                  # evaluator down -> caller falls back
        return {"tasks": [], "recipe": None, "recipe_args": {}, "uncovered": []}
    if isinstance(raw, tuple):
        raw = raw[0]
    try:
        decision = json.loads(raw[raw.find("{"):raw.rfind("}") + 1])
    except (ValueError, AttributeError):
        return {"tasks": [], "recipe": None, "recipe_args": {}, "uncovered": []}
    return _clean(decision)
