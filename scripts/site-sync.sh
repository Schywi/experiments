#!/usr/bin/env bash
# Assemble the site's content tree from the tracked documentation directories.
#
# The notes are the source of truth and live where they always have:
# research/, evolution/, benchmark/ and blog/. Quartz wants one content root, so
# this script mirrors those four trees into site/content/ (gitignored) together
# with README.md, ROADMAP.md and the landing page. Nothing is edited in place.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
content="${repo_root}/site/content"

rm -rf "${content}"
mkdir -p "${content}"

for dir in research evolution benchmark blog; do
  cp -rf --no-target-directory "${repo_root}/${dir}" "${content}/${dir}"
done

cp -f "${repo_root}/README.md" "${content}/README.md"
cp -f "${repo_root}/ROADMAP.md" "${content}/ROADMAP.md"
cp -f "${repo_root}/site/landing.md" "${content}/index.md"

find "${content}" -name '__pycache__' -type d -prune -exec rm -rf {} +
find "${content}" -name '*.pyc' -delete

printf 'content tree: %s files\n' "$(find "${content}" -type f | wc -l)"
