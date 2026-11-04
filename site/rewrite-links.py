#!/usr/bin/env python3
"""Rewrite relative links that point outside the published collections.

The site is built from research/, evolution/, benchmark/, blog/, README.md,
ROADMAP.md and the landing page. Notes legitimately link to code (`apps/`,
`config/`), to `AGENTS.md`, or to gitignored material (`docs/`); those targets
are not in the site, so a relative link becomes a 404. Each one is rewritten to
the file's GitHub URL on `cluster-evolution`, where the platform code lives.

Run by scripts/site-sync.sh over site/content/, never over the tracked notes.
"""

from __future__ import annotations

import pathlib
import re
import sys

BRANCH = "cluster-evolution"
REPO_URL = "https://github.com/Schywi/experiments"

LINK = re.compile(r"(?<!!)\[[^\]]*\]\((?!https?:|mailto:|#)([^)\s]+)\)")
IMAGE = re.compile(r"!\[[^\]]*\]\((?!https?:|mailto:|#)([^)\s]+)\)")


def rewrite(content_root: pathlib.Path) -> tuple[int, int]:
    rewritten = kept = 0
    for md in sorted(content_root.rglob("*.md")):
        if "__pycache__" in md.parts:
            continue
        text = md.read_text()
        rel_dir = md.parent.relative_to(content_root)

        def fix(target: str, match: re.Match, kind: str) -> str:
            nonlocal rewritten, kept
            path_part = target.split("#", 1)[0]
            if not path_part or path_part.startswith("/"):
                return match.group(0)
            resolved = (rel_dir / path_part).as_posix()
            parts: list[str] = []
            for part in resolved.split("/"):
                if part in ("", "."):
                    continue
                if part == "..":
                    if not parts:
                        parts.append("..")  # escaped the tree: not ours
                    else:
                        parts.pop()
                else:
                    parts.append(part)
            candidate = content_root.joinpath(*parts)
            if ".." not in parts and candidate.exists():
                kept += 1
                return match.group(0)
            rewritten += 1
            url = f"{REPO_URL}/blob/{BRANCH}/{path_part}"
            whole = match.group(0)
            return whole.replace(f"]({target})", f"]({url})")

        text = IMAGE.sub(lambda m: fix(m.group(1), m, "image"), text)
        text = LINK.sub(lambda m: fix(m.group(1), m, "link"), text)
        md.write_text(text)
    return rewritten, kept


if __name__ == "__main__":
    root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "site/content").resolve()
    rewritten, kept = rewrite(root)
    print(f"relative links: {kept} kept as site links, {rewritten} pointed at GitHub")
