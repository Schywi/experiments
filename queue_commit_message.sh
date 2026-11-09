#!/usr/bin/env bash
# The atomic commits for the queue-scheduling study. Run: bash queue_commit_message.sh [bd-issue-id]
#
# Scoped on purpose: this stages only the four queue-scheduling paths and never the persist
# work that is in the tree at the same time, so the commits land one intent each.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

STUDY=research/queue-scheduling
HANDOFF="$STUDY/HANDOFF.md"
NOTE="$STUDY/01-queueing-theory.md"
FOLDER_INDEX="$STUDY/README.md"
RESEARCH_INDEX=research/README.md

# --- the bd issue the commits name -------------------------------------------------------
ISSUE="${1:-}"
if [[ -z "$ISSUE" ]]; then
  if command -v bd >/dev/null 2>&1; then
    ISSUE="$(bd create "Queue scheduling: measure the wait split, simulate the policies, then one live A/B" \
      -t task -p 2 --json 2>/dev/null \
      | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)" || ISSUE=""
  fi
fi
if [[ -z "$ISSUE" ]]; then
  cat >&2 <<'USAGE'
No issue id, and bd could not mint one. Make the issue first, then pass it:

  bd create "Queue scheduling: measure the wait split, simulate the policies, then one live A/B" \
    -t task -p 2 --json
  bash queue_commit_message.sh <id>

Passing the id explicitly is also what keeps a rerun from creating a second issue.
USAGE
  exit 1
fi

# --- preconditions -----------------------------------------------------------------------
if ! git diff --cached --quiet; then
  echo "something else is already staged; this script decides what each commit carries:" >&2
  git diff --cached --name-status >&2
  echo "commit or unstage it first, then rerun." >&2
  exit 1
fi

for path in "$HANDOFF" "$NOTE" "$FOLDER_INDEX"; do
  [[ -f "$path" ]] || { echo "missing $path" >&2; exit 1; }
done

make_commit() {  # make_commit "<subject>" ; body on stdin
  local subject="$1" tmp
  tmp="$(mktemp)"
  { printf '%s (%s)\n\n' "$subject" "$ISSUE"; cat; } >"$tmp"
  git diff --cached --check          # whitespace, before anything is recorded
  git commit -F "$tmp"
  rm -f "$tmp"
}

# --- commit 1: the plan, which the folder index and the note both link --------------------
git add -- "$HANDOFF"
if git diff --cached --quiet; then
  echo "1/2 $HANDOFF is already committed; skipping"
else
  git diff --cached --stat
  make_commit "docs(queue): the handoff plan for a queue experiment that leaves the persist runs alone" <<'MSG'
research/queue-scheduling/HANDOFF.md is the work order: measure the three-way wait split from a
run that already exists, decide the algorithm in an in-process simulator that needs no cluster,
and only then put one nil-gated session.Options.Queue behind cmd/queue-lab, with its own
namespace, pool and output directory, so a queue run never takes a replica a persist run is
waiting on.

It records what the current ordering does: admission.Gate ranks by class then arrival with no
aging, so a bulk session behind steady normal traffic waits indefinitely, betterWaiting scans
every waiter per Acquire, and slots.Pool.Take wakes every waiter on one broadcast channel with
no ordering at all.

The isolation is stated as prohibitions, because the control has to keep running: no edit to
internal/runtime/admission or internal/runtime/slots while the simulator work is under way,
and no edit to the session-lab or persist-lab manifests and drivers.
MSG
fi

# --- commit 2: the theory, its folder index, and the research row that publishes it ------
# One commit: the folder index links the note, and research/README.md points the study at the
# folder index instead of at the handoff. Split, either index would reference a missing file.
git add -- "$NOTE" "$FOLDER_INDEX" "$RESEARCH_INDEX"
if git diff --cached --quiet; then
  echo "2/2 the theory note is already committed; skipping"
else
  git diff --cached --stat
  make_commit "docs(queue): the queueing theory the benchmark has to be judged against" <<'MSG'
research/queue-scheduling/01 states the model before any code exists. The recorded shape offers
3,885 sessions of concurrent demand (35 arrivals/s times a 111 s mean hold) to 304 measured
slots, so utilisation is about 12.8 and ordering cannot remove the latency: the makespan is
fixed by the work, and a policy decides which session absorbs it. Three jobs of 300, 120 and
30 s arriving together give mean response 360 s in arrival order and 210 s shortest-first,
which is the transfer every comparison has to report per class rather than average.

Three findings from the code change the plan in the handoff:

- there is a second admission gate, for boot concurrency, acquired at shared.go:170 before
  ClaimCreatedNS is stamped, and no session field records its wait. Note 22's queue wait p50 of
  0 ms beside an acquire p50 of 22 min means the wait sits in one of the two gates, and today's
  report cannot say which.
- report.Admission is computed and serialised but never printed, so the text summary omits the
  one wait the admission gate does record.
- LAS degenerates under non-preemptive leasing: a waiter holds no lease, so every waiter has
  attained service zero and the policy reduces to arrival order. 2LPS, which demotes on
  observed held time, is the deployable size-based policy, and it bounds what a long job
  classified as short costs the sessions behind it.

The note also gives each mechanism one question (token bucket admits, the slot pool bounds
leases, the scheduler orders, aging bounds starvation, the warm pool prepares capacity) and
records what a Redis-backed queue would buy against what it costs: a waiter-depth signal the
autoscaler currently cannot see, weighed against a hop per acquire and release and a wake-up
protocol where today there is one close(wait).

research/README.md points the study row at the folder index instead of the handoff.
MSG
fi

echo
git log --oneline -3
echo
git status --short -- "$STUDY" "$RESEARCH_INDEX"
echo
echo "nothing else was staged: the persist work in the tree is untouched."

rm -f -- "${BASH_SOURCE[0]}"
