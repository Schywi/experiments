"""Prometheus instrumentation for LLM calls — measurement only, no behaviour change.

Every call to the local llama.cpp server is recorded by **stage** so we can see
plan vs correlate vs plain chat:

  assistant_llm_requests_total{stage,model}
  assistant_llm_input_tokens_total{stage}
  assistant_llm_output_tokens_total{stage}
  assistant_llm_prefill_seconds{stage}
  assistant_llm_generation_seconds{stage}
  assistant_llm_request_seconds{stage}
  assistant_llm_ttft_seconds{stage}
  assistant_llm_prefill_tokens_per_second{stage}
  assistant_llm_generation_tokens_per_second{stage}

Inputs come straight from llama.cpp's `usage` and `timings` blocks, so the numbers
are the model's own (prompt_n/cache_n, predicted_n, prompt_ms, predicted_ms).
"""

from prometheus_client import (
    CONTENT_TYPE_LATEST,
    Counter,
    Histogram,
    generate_latest,
)

CONTENT_TYPE = CONTENT_TYPE_LATEST

# Seconds and tokens/second bucket layouts (cover ~50 ms .. 2 min, ~1 .. 100 tok/s).
_SEC = (0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 45, 60, 90, 120)
_TPS = (1, 2, 3, 4, 5, 6, 8, 10, 15, 20, 30, 40, 60, 100)

REQUESTS = Counter(
    "assistant_llm_requests_total", "LLM requests by stage", ["stage", "model"])
INPUT_TOKENS = Counter(
    "assistant_llm_input_tokens_total", "Prompt tokens processed, by stage", ["stage"])
OUTPUT_TOKENS = Counter(
    "assistant_llm_output_tokens_total", "Tokens generated, by stage", ["stage"])
PREFILL = Histogram(
    "assistant_llm_prefill_seconds", "Prompt/prefill duration", ["stage"], buckets=_SEC)
GENERATION = Histogram(
    "assistant_llm_generation_seconds", "Generation duration", ["stage"], buckets=_SEC)
REQUEST = Histogram(
    "assistant_llm_request_seconds", "Total LLM call duration", ["stage"], buckets=_SEC)
TTFT = Histogram(
    "assistant_llm_ttft_seconds", "Time to first token", ["stage"], buckets=_SEC)
PREFILL_TPS = Histogram(
    "assistant_llm_prefill_tokens_per_second", "Prefill tokens/second", ["stage"], buckets=_TPS)
GENERATION_TPS = Histogram(
    "assistant_llm_generation_tokens_per_second", "Generation tokens/second",
    ["stage"], buckets=_TPS)
TOOL_SECONDS = Histogram(
    "assistant_tool_seconds", "Read-only tool execution time", ["tool"], buckets=_SEC)


def record_tool(tool: str, ms: float) -> None:
    try:
        TOOL_SECONDS.labels(tool).observe(ms / 1000)
    except Exception:
        pass


def record(stage: str, model: str, *, input_tokens=None, output_tokens=None,
           prefill_ms=None, generation_ms=None, total_ms=None, ttft_ms=None) -> None:
    """Record one LLM call for `stage` (best-effort; never raises into the path)."""
    try:
        REQUESTS.labels(stage, model).inc()
        if input_tokens:
            INPUT_TOKENS.labels(stage).inc(input_tokens)
        if output_tokens:
            OUTPUT_TOKENS.labels(stage).inc(output_tokens)
        if prefill_ms is not None:
            PREFILL.labels(stage).observe(prefill_ms / 1000)
            if input_tokens:
                PREFILL_TPS.labels(stage).observe(input_tokens / (prefill_ms / 1000))
        if generation_ms is not None:
            GENERATION.labels(stage).observe(generation_ms / 1000)
            if output_tokens:
                GENERATION_TPS.labels(stage).observe(output_tokens / (generation_ms / 1000))
        if total_ms is not None:
            REQUEST.labels(stage).observe(total_ms / 1000)
        if ttft_ms is not None:
            TTFT.labels(stage).observe(ttft_ms / 1000)
    except Exception:
        pass


def record_payload(stage: str, model: str, payload: dict, total_ms=None, ttft_ms=None) -> None:
    """Record from a llama.cpp response (or stream aggregate) with usage/timings."""
    usage = (payload or {}).get("usage") or {}
    timings = (payload or {}).get("timings") or {}
    input_tokens = usage.get("prompt_tokens")
    output_tokens = usage.get("completion_tokens")
    if input_tokens is None and timings:
        # prompt_n excludes cache hits; add cache_n to get the real prompt size.
        input_tokens = (timings.get("prompt_n") or 0) + (timings.get("cache_n") or 0) or None
    if output_tokens is None and timings:
        output_tokens = timings.get("predicted_n") or None
    record(stage, model,
           input_tokens=input_tokens, output_tokens=output_tokens,
           prefill_ms=timings.get("prompt_ms"), generation_ms=timings.get("predicted_ms"),
           total_ms=total_ms, ttft_ms=ttft_ms)


def payload() -> bytes:
    return generate_latest()
