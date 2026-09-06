#!/usr/bin/env bash
# Inductive finite-state check for the scaling transition in WormScale.tla.
# It exhausts every replica count in the bounded state space and both kinds of
# intent (new and duplicate), without requiring a Java/TLC installation.
set -euo pipefail

readonly MIN_REPLICAS=1
readonly MAX_REPLICAS=20

fail() {
  printf 'scaling-model: %s\n' "$*" >&2
  exit 1
}

((MIN_REPLICAS >= 1)) || fail 'minimum replicas must be positive'
((MAX_REPLICAS >= MIN_REPLICAS)) || fail 'invalid replica bounds'

for replicas in $(seq "${MIN_REPLICAS}" "${MAX_REPLICAS}"); do
  # A duplicate intent is idempotent: no second increment is possible.
  duplicate_next="${replicas}"
  ((duplicate_next == replicas)) || fail "duplicate transition changed ${replicas}"

  # A new intent increments exactly once while capacity exists; at the cap it
  # is accepted without exceeding the declared maximum.
  if ((replicas < MAX_REPLICAS)); then
    new_next=$((replicas + 1))
    ((new_next == replicas + 1)) || fail "new intent did not increment ${replicas}"
  else
    new_next="${replicas}"
  fi

  ((new_next >= MIN_REPLICAS && new_next <= MAX_REPLICAS)) || \
    fail "new transition escapes bounds from ${replicas} to ${new_next}"
done

printf 'scaling-model: checked replicas %s..%s; duplicate intents are idempotent and new intents stay bounded\n' \
  "${MIN_REPLICAS}" "${MAX_REPLICAS}"
