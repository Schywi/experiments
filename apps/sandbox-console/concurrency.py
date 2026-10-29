"""In-process concurrency limit + bounded FIFO queue for CDP work.

Same shape as `apps/assistant/concurrency.py`, and the same scope caveat: the
console runs one uvicorn process with one worker and `replicas: 1`, so this is
an in-process bound, not a cluster-wide one.

Behaviour: at most `concurrency` screenshots/probes run at once; up to
`queue_size` more wait FIFO; anything further is refused immediately with
`QueueFull`, which the HTTP layer turns into `429` + `Retry-After`.
"""

import asyncio
from contextlib import asynccontextmanager


class QueueFull(Exception):
    """The bounded queue is at capacity; the caller should return 429."""


class BoundedFIFO:
    def __init__(self, concurrency: int, queue_size: int):
        self.concurrency = max(1, concurrency)
        self.queue_size = max(0, queue_size)
        self._capacity = self.concurrency + self.queue_size
        self._admitted = 0
        self._sem = asyncio.Semaphore(self.concurrency)

    @property
    def admitted(self) -> int:
        """Requests currently running or waiting."""
        return self._admitted

    @asynccontextmanager
    async def slot(self):
        if self._admitted >= self._capacity:
            raise QueueFull()
        self._admitted += 1
        try:
            await self._sem.acquire()
            try:
                yield
            finally:
                self._sem.release()
        finally:
            self._admitted -= 1
