#!/usr/bin/env python3
"""Rewrite relative links that point outside the published collections.

The site is built from research/, evolution/, benchmark/, blog/, README.md,
ROADMAP.md, pitch.md and the landing page. Notes legitimately link to code
(`apps/`, `config/`), to `AGENTS.md`, or to gitignored material (`docs/`); those
targets are not in the site, so a relative link becomes a 404. Each one is
rewritten to the file's GitHub URL on `cluster-evolution`, where the platform
code lives.

Raw-HTML anchors need a second treatment: the markdown link transformer does not
see `<a href="...">`, so a `href` that points at a published note is rewritten to
the note's site URL (the path without `.md`).

Run by scripts/site-sync.sh over site/content/, never over the tracked notes.
"""

from __future__ import annotations

import pathlib
import re
import sys

BRANCH = "cluster-evolution"
REPO_URL = "https://github.com/Schywi/experiments"

# Order matters: images first, then links (the link pattern skips `![`), then the
# raw-HTML anchors.
IMAGE = re.compile(r"!\[[^\]]*\]\((?!https?:|mailto:|#)([^)\s]+)\)")
LINK = re.compile(r"(?<!!)\[[^\]]*\]\((?!https?:|mailto:|#)([^)\s]+)\)")
HREF = re.compile(r'href="(?!https?:|mailto:|#)([^"]+\.md)"')


def normalise(rel_dir: pathlib.Path, path_part: str) -> tuple[list[str], bool]:
    """Resolve a link target to content-root parts; escaped=True if it left the tree."""
    parts: list[str] = []
    for part in (rel_dir / path_part).as_posix().split("/"):
        if part in ("", "."):
            continue
        if part == "..":
            if not parts:
                parts.append("..")
            else:
                parts.pop()
        else:
            parts.append(part)
    return parts, ".." in parts


def rewrite(content_root: pathlib.Path) -> tuple[int, int]:
    rewritten = kept = 0
    for md in sorted(content_root.rglob("*.md")):
        if "__pycache__" in md.parts:
            continue
        text = md.read_text()
        rel_dir = md.parent.relative_to(content_root)

        def fix(target: str, match: re.Match) -> str:
            nonlocal rewritten, kept
            path_part = target.split("#", 1)[0]
            if not path_part or path_part.startswith("/"):
                return match.group(0)
            parts, escaped = normalise(rel_dir, path_part)
            if not escaped and content_root.joinpath(*parts).exists():
                kept += 1
                return match.group(0)
            rewritten += 1
            url = f"{REPO_URL}/blob/{BRANCH}/{path_part}"
            return match.group(0).replace(f"]({target})", f"]({url})")

        def fix_href(match: re.Match) -> str:
            nonlocal rewritten, kept
            target = match.group(1)
            path_part, _, frag = target.partition("#")
            parts, escaped = normalise(rel_dir, path_part)
            suffix = f"#{frag}" if frag else ""
            if escaped or not content_root.joinpath(*parts).exists():
                rewritten += 1
                url = f"{REPO_URL}/blob/{BRANCH}/{path_part}{suffix}"
                return f'href="{url}"'
            kept += 1
            site = path_part[:-3]  # drop .md
            if site.endswith("/index"):
                site = site[: -len("/index")]
            return f'href="{site}{suffix}"'

        text = IMAGE.sub(lambda m: fix(m.group(1), m), text)
        text = LINK.sub(lambda m: fix(m.group(1), m), text)
        text = HREF.sub(fix_href, text)
        md.write_text(text)
    return rewritten, kept


if __name__ == "__main__":
    root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "site/content").resolve()
    rewritten, kept = rewrite(root)
    print(f"relative links: {kept} kept as site links, {rewritten} pointed at GitHub")
