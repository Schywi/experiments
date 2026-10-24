# LLM concurrency limit + bounded FIFO queue

Bounds how many LLM-bound requests the assistant will work on at once, and
applies backpressure (HTTP 429) instead of letting requests pile up unbounded.

## What it does

- At most **`llmConcurrency`** LLM-bound requests run at once (default **1**).
- Up to **`llmQueueSize`** more wait, **FIFO** (default **8**).
- Any further request is rejected immediately with **`429 Too Many Requests`**
  and a **`Retry-After`** header (default **5** seconds).

It exists because the single llama.cpp server has **one inference slot**: with no
in-process limit, concurrent users each grab a thread, fire a request at the
model, and block — the queue lives implicitly inside llama.cpp and the 40-thread
pool, with no fairness and no bound.

## Contract

Guarded endpoints (all LLM-bound work in `server.py`): `POST /chat`,
`POST /investigate`, `POST /plan`, and `POST /ui/message` (both `chat` and
`investigate` modes).

```
200  normal response
429  {"detail":"assistant is at capacity; retry shortly"}   Retry-After: 5
```

The JSON endpoints return the 429 verbatim. `POST /ui/message` (the htmx UI)
routes the same rejection through its existing error path, so the user sees an
"at capacity" bubble; the limiter still applies.

## Implementation (files)

| File | Role |
|---|---|
| `apps/assistant/concurrency.py` | `BoundedFIFO` — `asyncio.Semaphore(concurrency)` plus an admitted counter; FIFO; raises `QueueFull` when over capacity. Env config. |
| `apps/assistant/server.py` | `_llm_slot()` context manager converts `QueueFull` → 429 + `Retry-After`; wraps the four LLM-bound handlers. |
| `apps/assistant/llm_metrics.py` | `assistant_concurrency_rejected_total` counter (`record_rejection()`). |
| `apps/assistant/test_concurrency.py` | Unit tests (concurrency bound, FIFO order, reject-when-full). |

## Configuration

| Env | Chart value | Default | Meaning |
|---|---|---|---|
| `LLM_CONCURRENCY` | `concurrency.llmConcurrency` | `1` | Max concurrent LLM-bound requests. |
| `LLM_QUEUE_SIZE` | `concurrency.llmQueueSize` | `8` | Extra requests allowed to wait (FIFO). |
| `LLM_RETRY_AFTER_SECONDS` | `concurrency.retryAfterSeconds` | `5` | `Retry-After` header on 429. |

## How to verify

```bash
cd apps/assistant && python3 -m unittest test_concurrency -v     # limiter behaviour

# live: concurrency=1, queue=8 -> the 11th simultaneous request should 429
kubectl -n assistant port-forward svc/assistant 18080:8080 &
for i in $(seq 1 12); do
  curl -s -o /dev/null -w "%{http_code} " \
    -XPOST localhost:18080/investigate -H 'content-type: application/json' \
    -d '{"message":"is anything unhealthy?"}' &
done; wait
curl -s localhost:18080/metrics | grep assistant_concurrency_rejected_total
```

## Limits — the single-replica assumption ⚠️

- **This limiter is in-process.** It assumes **one replica and one uvicorn
  worker** — the current assistant Deployment (`replicas: 1`;
  `ENTRYPOINT uvicorn server:app`, no `--workers`). Under that assumption it is
  correct.
- **It is NOT shared across replicas or workers.** If the assistant is ever
  scaled to N replicas, each keeps its own limit and the effective total becomes
  ~N × `llmConcurrency`. Making the limit global would require shared state (a
  distributed queue or Redis) — deliberately out of scope here.
- **By design there is no Redis/Celery/Kafka.** For one process, `asyncio`
  primitives are sufficient; the repo's charter is to avoid distributed
  infrastructure without a concrete reason.
- Not addressed here: the `/chat` tool call still runs on the event loop, and
  `/chat` vs `/investigate` share one limit (no reserved slot for chat). Both are
  candidate follow-ups, not implemented.
