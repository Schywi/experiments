#!/usr/bin/env bash
# Vendor the pinned Quartz release into site/quartz.
#
# Quartz ships a 15 MB `quartz/util/emojimap.json` that would land in this
# repository's history for no benefit, so the framework tree is fetched from the
# pinned release tag at build time and gitignored. Everything this repository
# actually owns — quartz.config.ts, quartz.layout.ts, landing.md, package.json,
# the lockfile, tsconfig, LICENSE — is tracked next to this script.
set -euo pipefail

QUARTZ_REF="${QUARTZ_REF:-v4.5.2}"
QUARTZ_REPO="${QUARTZ_REPO:-https://github.com/jackyzha0/quartz}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
upstream="${here}/.upstream"

if [ ! -d "${upstream}/.git" ]; then
  echo "fetching quartz ${QUARTZ_REF}"
  rm -rf "${upstream}"
  git clone --quiet --depth 1 --branch "${QUARTZ_REF}" "${QUARTZ_REPO}" "${upstream}"
fi

rm -rf "${here}/quartz"
cp -rf "${upstream}/quartz" "${here}/quartz"

# Our stylesheet overrides live outside the fetched tree so they survive the
# refresh above; Quartz loads quartz/styles/custom.scss as its entry stylesheet.
if [ -f "${here}/custom.scss" ]; then
  cp -f "${here}/custom.scss" "${here}/quartz/styles/custom.scss"
fi

# The carousel controller is served as a static asset, not written inline in the
# markdown: the HTML pipeline escapes quotes inside raw script text.
if [ -f "${here}/carousel.js" ]; then
  cp -f "${here}/carousel.js" "${here}/quartz/static/carousel.js"
fi

echo "quartz ${QUARTZ_REF} vendored into site/quartz"
