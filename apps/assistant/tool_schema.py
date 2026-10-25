"""Strict argument schemas for the read-only tools.

A tool call is validated against its schema before it runs. Missing required
arguments, unknown tools and wrong types are rejected with an error string, and
that rejection is recorded as a failed fact. No dependency on the cluster, so this
is unit-testable alone.
"""

from typing import Optional

# name -> {arg: (type, required)}
SCHEMAS = {
    "get_pods": {"namespace": (str, False)},
    "get_pod_logs": {"namespace": (str, True), "pod": (str, True),
                     "tail_lines": (int, False)},
    "get_events": {"namespace": (str, False)},
    "search_logs": {"namespace": (str, True), "contains": (str, True)},
    "get_topology": {"namespace": (str, False)},
    "get_topology_snapshot": {"question": (str, False)},
    "query_prometheus": {"query": (str, True)},
    "search_knowledge": {"query": (str, True)},
    "remember": {"text": (str, True), "tag": (str, False)},
    "recall": {"query": (str, True), "k": (int, False)},
}


def check_args(name: str, args) -> tuple:
    """Return (clean_args, error). error is None when the call is valid."""
    spec = SCHEMAS.get(name)
    if spec is None:
        return None, f"unknown tool {name!r}"
    if args is None:
        args = {}
    if not isinstance(args, dict):
        return None, "args must be an object"

    clean = {}
    for key, value in args.items():
        if key not in spec:
            continue                                  # drop unknown keys
        want, _required = spec[key]
        if isinstance(value, want) and not isinstance(value, bool):
            clean[key] = value
        elif want is int and isinstance(value, str) and value.strip().lstrip("-").isdigit():
            clean[key] = int(value)                   # tolerate "100"
        else:
            return None, f"arg {key!r} must be {want.__name__}"

    for key, (_want, required) in spec.items():
        if required and key not in clean:
            return None, f"missing required arg {key!r}"
    return clean, None


def is_known(name: str) -> bool:
    return name in SCHEMAS


def names() -> list:
    return sorted(SCHEMAS)
