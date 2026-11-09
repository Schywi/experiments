#!/usr/bin/env bash
# The atomic commits for autoscaler v2. Run: bash queue_v2_commit_message.sh [bd-issue-id]
#
# Unlike the earlier one-shot scripts, this file stays in the repository: the build and
# the run that produced a measurement have to be reproducible from the tree, so the
# scripts and the commit series that landed them are part of the record.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

APP=apps/session-lab
SLOTS=$APP/internal/runtime/slots
SESS=$APP/internal/runtime/session
POOL=$APP/internal/infra/pool
QL=$APP/cmd/queue-lab
CONFIG_SL=config/session-lab
CONFIG_QL=config/queue-lab

ISSUE="${1:-}"
if [[ -z "$ISSUE" ]]; then
  if command -v bd >/dev/null 2>&1; then
    ISSUE="$(bd create "Autoscaler v2: published demand, a provisioning bound, memory terms, tagged arms" \
      -t feature -p 1 --json 2>/dev/null \
      | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)" || ISSUE=""
  fi
fi
if [[ -z "${ISSUE}" ]]; then
  cat >&2 <<'USAGE'
No issue id, and bd could not mint one. Make the issue, then pass it:

  bd create "Autoscaler v2: published demand, a provisioning bound, memory terms, tagged arms" \
    -t feature -p 1 --json
  bash queue_v2_commit_message.sh <id>
USAGE
  exit 1
fi

if ! git diff --cached --quiet; then
  echo "something else is already staged:" >&2
  git diff --cached --name-status >&2
  exit 1
fi

make_commit() {  # make_commit "<subject>" ; body on stdin
  local subject="$1" tmp
  tmp="$(mktemp)"
  { printf '%s (%s)\n\n' "$subject" "${ISSUE}"; cat; } >"${tmp}"
  git diff --cached --check
  git commit -F "${tmp}"
  rm -f -- "${tmp}"
}

step() {  # step <n> <subject> ; body on stdin
  local n="$1" subject="$2"
  if git diff --cached --quiet; then
    echo "${n}/9 already committed; skipping: ${subject}"
    cat >/dev/null
    return
  fi
  git diff --cached --stat
  make_commit "$subject"
}

# --- 0b: the build fix, first ---------------------------------------------------------------------------------
git add -- scripts/build-session-lab-images.sh research/queue-scheduling/06-autoscaler-v2.md
step 0b "fix(scripts): the in-cluster build proves the remote has the branch and the work" <<'MSG'
scripts/build-session-lab-images.sh conflated two arguments: the image tag to produce and the
branch to clone. Run with no arguments it asked kaniko for refs/heads/v2, which is a tag name and
not a branch, and the build died after the full timeout with "error resolving source context:
reference not found" -- kaniko saying it cloned the repository and could not find the ref. The
tail of that log is kaniko's help output, so the reason was 3,000 lines above where anyone looks.

It also missed the quieter failure: when the branch is on the remote but the commits are not,
kaniko builds the older tree and pushes it under the requested tag, and a v2 run would then
measure v1 and say v2. Both are refused before a Job is submitted: the branch is checked with
git ls-remote, the commits with git log origin/<branch>..HEAD over apps/session-lab, and a dirty
tree under apps/session-lab for the same reason. --allow-unpushed and --allow-dirty build an
older tree deliberately.

The wait is now on the Job's Failed/Complete condition with the first twenty log lines on failure,
not on a ten-minute clock followed by the last sixty.
MSG

# --- 0: the race families this suite had at HEAD ---------------------------------------
# Not part of v2, and first in the series so it can be dropped on its own. go test ./... -race
# went from red to green here; note 28 records what was wrong.
git add -- "$APP/internal/infra/sandbox/sandbox.go" "$APP/internal/infra/kube/kube.go" \
  "$APP/internal/infra/kube/watch.go" "$SESS/boot.go" "$SESS/shared.go" "$SESS/run.go" \
  "$SESS/run_test.go"
step 0 "fix(session): guard a shared sandbox's counters, and stop asserting the host's speed" <<'MSG'
go test ./... -race was red on a family of races that predate this work: one goroutine per session
incremented sandbox.Instance.Sessions and the boot sampler wrote BootCPUPeakCores while the store
and the report read them, and the kube adapter wrote placement fields on the same shared object.

sandbox.Instance now owns a mutex and the methods that read and write it: CountSession,
SetBootPeak, Counters, Placement, SetReadyAt, SuspendedNow, plus Lock/Unlock for the adapter that
fills several fields from one API read. fillFromSandbox holds that lock across the whole block,
persistInstance takes one snapshot so a stored row is a single instant, and the four session sites
call the method instead of touching the field. No behaviour changes: the same number is recorded at
the same moment in a session's life, which is why the density figures stay comparable with note 22.

TestReuseServesManySessionsOnOneInstance also had to stop depending on machine speed. It asserted
that a reuse policy reuses an instance, but with 12 sessions 2 ms apart and 5 ms holds that only
happens when the fake is fast: under -race the sessions overlap, each needs its own browser, and
reuse never occurs. It now runs with Workers: 1, which is the condition its name describes.

The whole module passes under -race after this, so a red run is a regression rather than the
expected state.
MSG

# --- 1 ---------------------------------------------------------------------------------
git add -- "$SLOTS/slots.go" "$SLOTS/slots_test.go"
step 1 "feat(slots): bound instance creations in flight, one at a time until now" <<'MSG'
slots.Pool admits one Provide call at a time, and Provide is the runner's
acquireInstance, which creates the claim and then waits for readiness. The cheap half and
the forty-second half therefore share one counter, and there was no way to raise it: the
fleet grew eight slots per forty seconds no matter how many sessions were waiting.

Options.MaxProvisioning carries that bound, defaulting to one so every existing run is
unchanged. TestMaxProvisioningAdmitsKSessionsAtOnce holds the provider open and asserts
three creations run at once at k=3 and that the fourth is parked; the default test pins one.

Measured on the recorded shape, a bound of four turns a 29.3-minute ramp into 7.3 minutes,
which takes the makespan from 30.4 to 19.4 minutes against a 15.8-minute drain floor.
MSG

# --- 2 ---------------------------------------------------------------------------------
git add -- "$SESS/demand.go" "$SESS/run.go" "$SESS/pool.go" \
  "$APP/internal/platform/model/model.go" "$APP/internal/platform/config/config.go"
step 2 "feat(session): publish the runner's live queue depth, and carry the provisioning bound" <<'MSG'
A claim cannot carry demand: a session waiting in the admission gate or the slot pool has no
claim at all, and a claim bound to a booting warm-pool sandbox is bound rather than pending.
The autoscaler's demand term was therefore (ready+pending)/contexts, which describes the fleet
it already has -- the mechanism behind the measured "Pending = 0/1 while thousands waited".

Options.DemandSink publishes live, waiting, the gate counts and per-class waits once a second.
One second is far below the cost of the action it drives: the autoscaler reconciles every five
seconds and a pod takes about forty to boot. Nil publishes nothing, so every earlier run is
unchanged, and a publish failure is logged rather than allowed to kill a run.

The same commit carries --max-provisioning and --publish-demand into RunConfig, and hands the
bound to the slot pool when it is built.
MSG

# --- 3 ---------------------------------------------------------------------------------
git add -- "$POOL/autoscale.go" "$POOL/autoscale_test.go" "$APP/internal/infra/measure/node.go" \
  "$APP/cmd/pool-autoscaler/main.go"
step 3 "feat(pool): demand from the runner, memory in the pressure guard, and the arm in the record" <<'MSG'
--demand-source gains the value published, which reads the runner's counts and falls back to
claims until the first publish arrives, marking demand_fallback in the published settings so a
report cannot read a fallback as a published number. claims stays the default and is what every
earlier autoscaler did.

NodeLoad gains MemAvailableMB, MemTotalMB and MemoryPressureFull. The existing guard watches
CPU utilisation and CPU PSI, and the resource that stopped the older run was memory: at 38
instances the host held 14.3 GiB of RAM plus 7.7 GiB of zram while CPU sat at 1.97 of 6 cores.
Both memory terms default to off, so v2 with v1's arguments behaves as v1.

The published settings carry image_tag, max_provisioning, demand_source, demand_pods and the
memory readings: a run has to be able to say which arm it ran, and a tag is the only thing that
does that after the fact.

Deliberately not shipped: --demand-source=pool-status. replicas minus readyReplicas is a
deficit, and sizing on a deficit holds the pool where it is. Shipping it would have produced a
switch that always "worked" and never changed anything.
MSG

# --- 4 ---------------------------------------------------------------------------------
git add -- "$APP/internal/queue/trace.go" "$APP/internal/queue/trace_test.go" "$QL"
step 4 "feat(queue-lab): a two-class partition, the live demand sink, and the arm flags" <<'MSG'
--classes two maps a hold at or below --class-normal to normal and everything above to bulk,
leaving batch empty on purpose rather than unreachable by accident. That is the partition the
owner chose, with prio-aging as its discipline: strict normal ahead of bulk, plus a wait credit
so bulk cannot be starved.

queue-lab also publishes the runner's depth when --publish-demand is set, through the
ConfigMap sink in cmd/queue-lab/demand.go, and passes --max-provisioning into the run's
configuration. Both are forwarded unchanged, so the lab can run an arm without owning the
mechanism.
MSG

# --- 6 ---------------------------------------------------------------------------------
git add -- "$CONFIG_QL/templates/autoscaler.yaml" "$CONFIG_QL/values.yaml"
step 5 "feat(queue-lab): its own autoscaler, pinned to a tagged image" <<'MSG'
config/queue-lab gains a ServiceAccount, Role, RoleBinding and Deployment for an autoscaler of
its own, against queue-lab-pool in namespace queue-lab. Nothing here touches sandbox-demo, so a
capacity experiment and a persist run cannot take each other's replicas.

The image tag names the arm. queue-lab's values pin the tag, and the arguments come from
values so an arm is a manifest edit: demand source, demand ConfigMap, provisioning bound, the
memory thresholds, and --image-tag so the autoscaler publishes what it is. config/session-lab
follows with the same flags and an immutable tag in place of the mutable local.

contextsPerBrowser goes to 8 in config/session-lab: with published demand the divisor is the
payload's own, and with the claims fallback the old value of 1 inflated demand eightfold -- a
miscount the self-referential demand signal was hiding.
MSG

# --- 6 ---------------------------------------------------------------------------------
git add -- "$CONFIG_SL/values.yaml" "$CONFIG_SL/templates/deployment.yaml"
step 6 "feat(session-lab): v2's arguments, an immutable tag, and a chart that can be applied" <<'MSG'
The Deployment's pod template included both session-lab.selector and session-lab.labels, and the
latter already carries app.kubernetes.io/name, so the rendered manifest had the key twice in one
map: kubeconform rejected it with 'key "app.kubernetes.io/name" already set in map' and kubectl
apply refuses such a manifest outright. helm lint passed, which is why it survived. Dropping the
selector include inside the pod template fixes it, and the chart now validates 5 of 5.

The same template passes v2's arguments -- demand source, demand ConfigMap, provisioning bound,
image tag and the two memory thresholds -- and values.yaml pins an immutable tag in place of the
mutable local one, because the tag is what names the arm.

contextsPerBrowser goes to 8: with published demand the divisor is the payload's own, and with
the claims fallback the old value of 1 inflated demand eightfold, a miscount the self-referential
demand signal was hiding.
MSG

# --- 7 ---------------------------------------------------------------------------------
git add -- scripts/build-session-lab-images.sh scripts/queue-v2-run.sh scripts/queue-experiment.sh
step 7 "feat(scripts): a kaniko build per arm, and the driver for the v2 run" <<'MSG'
scripts/build-session-lab-images.sh builds the image in-cluster with Kaniko and tags it per
arm: v1 is the sizing that shipped before this work, v2 carries the changes. The tag is not
reused, because a run records its tag and a reused tag makes two behaviours look identical.

scripts/queue-v2-run.sh runs one arm and proves which arm it is before starting: it reads the
tag back from the autoscaler's own ConfigMap and refuses to run when it does not match, checks
the pool's replicas and ready count, refuses to start with leaked claims, then prints the
three-way wait split and the arm's published settings afterwards.

Both stay in the repository on purpose. The queue driver's boundary check gains a v2 category,
so the files this work reaches in slots, the pool autoscaler and the run configuration are
labelled rather than hidden.
MSG

# --- 8 ---------------------------------------------------------------------------------
git add -- research/queue-scheduling/06-autoscaler-v2.md research/queue-scheduling/README.md \
  research/session-lifecycle-elasticity/25-capacity-ramp-and-provisioning.md \
  research/session-lifecycle-elasticity/27-autoscaler-signals.md \
  research/session-lifecycle-elasticity/28-race-detector-audit.md AGENTS.md
step 8 "docs(queue): autoscaler v2, its tagged arms, and what the two-class run must report" <<'MSG'
research/queue-scheduling/06 records what v2 changes and why every flag's zero value reproduces
v1, how an arm is identified afterwards (image tag, ConfigMap readback, the run's own JSON),
the normal/bulk prio-aging arm, the two scripts, and the metrics the v2 run has to report --
including the redundant-boot rate, which is "my claim was worthless" as a number.

It also records what v2 leaves out and why: the v1/v2 A/B (deferred by the owner, v1's numbers
already known), the pool-status demand source (a deficit is not demand), the warm redefinition
(it moves a published metric) and Redis (note 04).

Notes 25 and 27 gain the pointer that the code shipped and the sweep has not run. Note 28 and
AGENTS.md stop describing a red -race suite, because it is green now.
MSG

echo
git log --oneline -11
echo
git status --short -- "$APP" "$CONFIG_SL" "$CONFIG_QL" scripts research
echo
echo "The series stays in the tree; nothing here removes itself."

if [[ "${1:-}" != "--keep" ]]; then
  echo "Pass --keep to silence this line: the script is part of the repository now."
fi
