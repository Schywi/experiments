#!/usr/bin/env python3
"""Generate anonymous memory pressure and measure what the kernel does with it.

No packages: mmap + a write per page is enough to force real anonymous pages,
which is the only kind swap/zram can move. Page cache would not exercise either.

Two modes, because they answer different questions:
  --target-mib N   allocate exactly N MiB, hold, exit 0       (does it fit?)
  --target-mib 0   allocate in blocks until it fails           (where is the wall?)

Everything printed as JSON so the caller does not parse prose.
"""
import argparse
import json
import mmap
import os
import resource
import sys
import time

PAGE = 4096
BLOCK = 32 * 1024 * 1024  # 32 MiB per mmap keeps the loop cheap


def meminfo():
    out = {}
    with open("/proc/meminfo") as fh:
        for line in fh:
            key, _, rest = line.partition(":")
            try:
                out[key] = int(rest.strip().split()[0]) // 1024  # MiB
            except (ValueError, IndexError):
                continue
    return out


def vmstat():
    out = {}
    with open("/proc/vmstat") as fh:
        for line in fh:
            key, _, val = line.partition(" ")
            if key in ("pswpin", "pswpout", "pgmajfault", "oom_kill", "thp_fault_alloc"):
                try:
                    out[key] = int(val)
                except ValueError:
                    pass
    return out


def zram():
    """zram mm_stat: orig_data_size, compr_data_size, mem_used_total, ..."""
    path = "/sys/block/zram0/mm_stat"
    if not os.path.exists(path):
        return None
    with open(path) as fh:
        fields = fh.read().split()
    try:
        return {
            "orig_bytes": int(fields[0]),
            "compr_bytes": int(fields[1]),
            "mem_used_bytes": int(fields[2]),
            "ratio": (int(fields[0]) / int(fields[1])) if int(fields[1]) else 0.0,
        }
    except (IndexError, ValueError):
        return None


def pressure(target_mib, hold_s, report_every):
    """Allocate until the target is reached or allocation fails."""
    blocks = []
    allocated = 0
    started = time.time()
    failure = None
    deadline = started + hold_s if hold_s else None

    try:
        while target_mib == 0 or allocated < target_mib:
            chunk = min(BLOCK, (target_mib - allocated) * 1024 * 1024) if target_mib else BLOCK
            if chunk <= 0:
                break
            block = mmap.mmap(-1, chunk)
            # Touch every page: untouched mmap is just address space, not memory.
            for offset in range(0, chunk, PAGE):
                block[offset] = 1
            blocks.append(block)
            allocated += chunk // (1024 * 1024)
            if deadline is not None and time.time() > deadline:
                break
    except (MemoryError, OSError) as err:
        failure = f"{type(err).__name__}: {err}"

    alloc_seconds = time.time() - started

    # Hold the footprint so the caller can sample swap/zram while it is live,
    # and so the kernel has to keep the pages resident or displaced.
    peak_swap = 0
    peaks = []
    hold_until = time.time() + hold_s
    while time.time() < hold_until:
        info = meminfo()
        used = info.get("SwapTotal", 0) - info.get("SwapFree", 0)
        peak_swap = max(peak_swap, used)
        peaks.append(used)
        time.sleep(min(1.0, max(0.05, hold_until - time.time())))

    return {
        "allocated_mib": allocated,
        "alloc_seconds": round(alloc_seconds, 3),
        "mib_per_second": round(allocated / alloc_seconds, 1) if alloc_seconds else 0,
        "failure": failure,
        "swap_used_peak_mib": peak_swap,
        "swap_used_samples": len(peaks),
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--target-mib", type=int, default=0, help="0 = allocate until failure")
    ap.add_argument("--hold", type=int, default=15, help="seconds to hold the footprint")
    ap.add_argument("--label", default="")
    args = ap.parse_args()

    before_mem, before_vm = meminfo(), vmstat()
    result = pressure(args.target_mib, args.hold, 1)
    after_mem, after_vm = meminfo(), vmstat()

    payload = {
        "label": args.label,
        "target_mib": args.target_mib,
        "mem_total_mib": before_mem.get("MemTotal"),
        "mem_available_before_mib": before_mem.get("MemAvailable"),
        "mem_available_after_mib": after_mem.get("MemAvailable"),
        "swap_total_mib": before_mem.get("SwapTotal"),
        "pswpin_pages": after_vm.get("pswpin", 0) - before_vm.get("pswpin", 0),
        "pswpout_pages": after_vm.get("pswpout", 0) - before_vm.get("pswpout", 0),
        "pgmajfault": after_vm.get("pgmajfault", 0) - before_vm.get("pgmajfault", 0),
        "oom_kill": after_vm.get("oom_kill", 0) - before_vm.get("oom_kill", 0),
        "max_rss_mib": resource.getrusage(resource.RUSAGE_SELF).ru_maxrss // 1024,
        "zram": zram(),
    }
    payload.update(result)
    print(json.dumps(payload, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
