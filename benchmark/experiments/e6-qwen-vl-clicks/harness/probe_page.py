"""Read a page's live DOM through the console's own CDP route, and print what a
task set needs: each visible button's text, data-target, centre and size.

    CONSOLE_URL=http://192.168.0.250:8080 \
    ROUTER_URL=http://10.43.71.0:8080 \
    ROUTER_TOKEN="$(kubectl -n sandbox-console create token sandbox-console)" \
      python3 harness/probe_page.py <sandbox-id>

Why this exists: the DOM arms resolve targets from the accessibility tree, and the
points in a task set have to be read from the same page the browser is showing.
A frozen PNG from another day does not describe the live page's geometry, and E6's
first dom-unique run showed exactly that failure (`dom-no-match` on every task,
because the cards in the 2026-10-26 frame no longer exist on the live grid).

Used to build `tasks/targets/labels.json`; it changes nothing on the page.
"""

from __future__ import annotations

import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "controller"))

import console  # noqa: E402

EXPR = r"""
(() => {
  const out = {url: location.href, title: document.title,
               scrollY: Math.round(window.scrollY),
               viewport: {w: innerWidth, h: innerHeight},
               buttons: []};
  for (const b of document.querySelectorAll('button, a, h3')) {
    const r = b.getBoundingClientRect();
    if (r.width < 4 || r.height < 4) continue;
    if (r.bottom <= 0 || r.top >= innerHeight) continue;
    out.buttons.push({
      tag: b.tagName.toLowerCase(),
      text: (b.textContent || '').trim().replace(/\s+/g, ' '),
      target: (b.dataset && b.dataset.target) || '',
      centre: {x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2)},
      size: {w: Math.round(r.width), h: Math.round(r.height)}
    });
  }
  return JSON.stringify(out);
})()
"""


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    state = console.evaluate(sys.argv[1], EXPR)
    if not isinstance(state, dict):
        print(f"unexpected reply: {state!r}", file=sys.stderr)
        return 1
    print(f"url: {state['url']}")
    print(f"viewport: {state['viewport']['w']}x{state['viewport']['h']} "
          f"scrollY={state['scrollY']}")
    print(f"{'tag':5s} {'centre':>12s} {'size':>9s}  text / data-target")
    for item in state["buttons"]:
        centre = f"({item['centre']['x']},{item['centre']['y']})"
        size = f"{item['size']['w']}x{item['size']['h']}"
        print(f"{item['tag']:5s} {centre:>12s} {size:>9s}  {item['text']!r}"
              f"  target={item['target']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
