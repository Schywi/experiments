#!/usr/bin/env bash
# Commit everything in the working tree. Run: bash commit_matrix.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

# -A: an uncommitted change is a defect, so this stages the whole tree.
git add -A
# Except this script, which deletes itself at the end.
git reset -q -- "${BASH_SOURCE[0]##*/}" 2>/dev/null || true
git diff --cached --stat

if git diff --cached --quiet; then
  echo "nothing to commit; the tree is already clean"
  rm -f -- "${BASH_SOURCE[0]}"
  exit 0
fi

git commit -F - <<'MSG'
feat(persist): a dataset matrix, with IndexedDB and Cache Storage behind it (experiments-mj0)

--seed-mix names a corpus instead of one size: local-64k:10,idb-256k:3,cache-1m:1,cookies-4:5
and nothing's label for a session that carries no seeded state. Each label names a surface and
a size, weights decide how many sessions carry it, and the assignment is deterministic by
session id, so two runs at one mix inject the same dataset into the same sessions.
--seed-bytes and --seed-cookies survive as sugar for a single-dataset mix.

IndexedDB and Cache Storage are now captured and restored. Neither can be handled by CDP alone
— IndexedDB has a read-only domain and Cache Storage has none — so both are read and written
through the page with Runtime.evaluate: databases and stores are recreated, records put,
cached responses rebuilt from base64 bodies, and the page reloaded so it runs with that state
present. Restore therefore has two shapes: cookies and localStorage before the first
navigation, these two after it.

A capture now reports per-dataset sessions and bytes, and a restore reports each surface
separately (cookies, items, IndexedDB records, Cache Storage entries) with an `incomplete`
count, because "ok" across a mix would otherwise hide a surface that was silently dropped.

Three defects found while measuring this on example.com:
- the write script kept its self-invoking (), so appending the argument called a promise:
  "TypeError: ... is not a function";
- the read script emitted its cache section as `cache` while the blob expects
  `cache_storage`, and JSON decoding dropped it without a word;
- a surface that failed to read produced an empty result with no trace, so the blob now
  carries cache_error, indexeddb_error and a cache_probe saying what the page made of the API.

Measured on example.com, run 20261109T153249Z, two sessions: idb-64k 49,393 B restored 8 of
8 records; cache-64k 61,865 B restored 8 of 8 entries; restore ok 2, incomplete 0, mean 384 ms.
MSG

git log --oneline -2
echo
git status --short

rm -f -- "${BASH_SOURCE[0]}"
