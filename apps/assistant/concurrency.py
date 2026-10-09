"""In-process concurrency limit + bounded FIFO queue for the assistant.

SCOPE — single replica, single worker. The assistant runs one uvicorn process
with one worker (`ENTRYPOINT ["uvicorn", "server:app", ...]`, no `--workers`) at
`replicas: 1`. This limiter is an in-process `asyncio` primitive, so it bounds
concurrency for **that one process only**. It is deliberately NOT shared across
replicas or workers: if the assistant is ever scaled out, each process keeps its
own limit and the effective total multiplies. That is an intentional, documented
limitation — no Redis, Celery, or distributed queue.

Behaviour: at most `concurrency` LLM-bound requests run at once; up to
`queue_size` more wait (FIFO); any further request is rejected immediately with
`QueueFull`, which the HTTP layer turns into `429 Too Many Requests` +
`Retry-After`.
"""

import asyncio
import os
from contextlib import asynccontextmanager


class QueueFull(Exception):
    """The bounded queue is at capacity; the caller should return 429."""


def _int_env(name: str, default: int) -> int:
    try:
        return int(os.environ.get(name, default))
    except (TypeError, ValueError):
        return default


# Configuration (env; surfaced as `concurrency:` in chart/values.yaml).
LLM_CONCURRENCY = max(1, _int_env("LLM_CONCURRENCY", 1))
LLM_QUEUE_SIZE = max(0, _int_env("LLM_QUEUE_SIZE", 8))
RETRY_AFTER_SECONDS = max(1, _int_env("LLM_RETRY_AFTER_SECONDS", 5))


class BoundedFIFO:
    """At most `concurrency` active slots; up to `queue_size` waiting; FIFO.

    Admission is checked synchronously (no `await` before the increment), so on a
    single event loop there is no race between the capacity check and the count.
    `asyncio.Semaphore` wakes waiters in FIFO order.
    """

    def __init__(self, concurrency: int, queue_size: int):
        self.concurrency = max(1, concurrency)
        self.queue_size = max(0, queue_size)
        self._capacity = self.concurrency + self.queue_size
        self._admitted = 0                      # running + waiting
        self._sem = asyncio.Semaphore(self.concurrency)

    @property
    def admitted(self) -> int:
        """Requests currently running or waiting (admitted into the limiter)."""
        return self._admitted

    @asynccontextmanager
    async def slot(self):
        if self._admitted >= self._capacity:
            raise QueueFull()
        self._admitted += 1
        try:
            await self._sem.acquire()           # FIFO: waiters wake in order
            try:
                yield
            finally:
                self._sem.release()
        finally:
            self._admitted -= 1


# One shared limiter for every LLM-bound request handled by this process.
LLM_LIMITER = BoundedFIFO(LLM_CONCURRENCY, LLM_QUEUE_SIZE)
