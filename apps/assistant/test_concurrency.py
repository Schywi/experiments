"""Unit tests for the in-process bounded FIFO limiter (pure stdlib).

Run: cd apps/assistant && python3 -m unittest test_concurrency -v
"""

import asyncio
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from concurrency import BoundedFIFO, QueueFull  # noqa: E402


class BoundedFIFOTest(unittest.IsolatedAsyncioTestCase):
    async def test_rejects_when_full_and_is_fifo(self):
        b = BoundedFIFO(concurrency=1, queue_size=1)
        order = []

        async def work(tag):
            async with b.slot():
                order.append(("start", tag))
                await asyncio.sleep(0.05)
                order.append(("end", tag))

        t1 = asyncio.create_task(work("A"))
        await asyncio.sleep(0.01)
        self.assertEqual(b.admitted, 1)          # A running
        t2 = asyncio.create_task(work("B"))
        await asyncio.sleep(0.01)
        self.assertEqual(b.admitted, 2)          # B queued
        with self.assertRaises(QueueFull):       # capacity = 1 + 1
            async with b.slot():
                pass
        await asyncio.gather(t1, t2)
        self.assertEqual(order, [("start", "A"), ("end", "A"),
                                 ("start", "B"), ("end", "B")])
        self.assertEqual(b.admitted, 0)          # released

    async def test_concurrency_is_bounded(self):
        b = BoundedFIFO(concurrency=2, queue_size=10)
        state = {"running": 0, "peak": 0}

        async def work():
            async with b.slot():
                state["running"] += 1
                state["peak"] = max(state["peak"], state["running"])
                await asyncio.sleep(0.02)
                state["running"] -= 1

        await asyncio.gather(*[work() for _ in range(12)])
        self.assertLessEqual(state["peak"], 2)
        self.assertEqual(b.admitted, 0)

    async def test_zero_queue_runs_only_concurrency(self):
        b = BoundedFIFO(concurrency=1, queue_size=0)
        async with b.slot():
            with self.assertRaises(QueueFull):   # no queue -> immediate reject
                async with b.slot():
                    pass


if __name__ == "__main__":
    unittest.main()
