#!/bin/sh
set -eu

worker=worker.lua
test -f "$worker"

# These checks run on a bare POSIX shell; neither Lua nor a WASI runtime is
# needed to validate the public worker contract and its resource bounds.
grep -Fq 'required_env("WORM_ID")' "$worker"
grep -Fq 'required_env("VECTOR_EVENTS_URL")' "$worker"
grep -Fq 'required_env("REPLICATOR_URL")' "$worker"
grep -Fq 'local MAX_BATCH = 10' "$worker"
grep -Fq 'local MAX_RETRIES = 5' "$worker"
grep -Fq '"/v1/replication-intents"' "$worker"
grep -Fq 'worm_id' "$worker"
grep -Fq 'sequence' "$worker"
grep -Fq 'occurred_at' "$worker"
! grep -Eq 'host[.](kubernetes|kubectl|serviceaccount)' "$worker"
test -f Containerfile
grep -Fq 'FROM scratch' Containerfile
grep -Fq 'dist/worm.component.wasm' Containerfile
