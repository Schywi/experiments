#!/usr/bin/env bash
# Capacity ladder: how many concurrent sessions does this arm hold?
#
# One class per invocation. `CLASS=swap-off` forbids swap for the sandbox pods
# (memory.swap.max=0), `CLASS=swap-on` grants SWAP_ALLOWANCE per pod. Running one
# class per arm is deliberate: an arm whose VM has no swap device at all gains
# nothing from a "swap granted" ladder, and it doubled the runtime.
#
# The class is enforced on the pods that actually serve the run, not on whatever
# existed a moment earlier: the pool is recycled, the limit is written once the
# pods are Ready, re-applied while the load runs (the pool can refill mid-run),
# and verified before a single session is allowed to start.
#
# A point counts as served when the pool reaches Ready and a WINDOW-long load at
# that concurrency finishes with no failed session. The first point that does not
# is the ceiling, and the ladder stops there — going past it wedges the control
# plane instead of measuring anything.
#
# Rungs stay <= 6: at 8 the claim watch in session-lab rejects its own watch
# options (resourceVersionMatch: Forbidden) and that failure is indistinguishable
# from a capacity boundary. That code is out of scope.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

CLASS="${CLASS:-swap-off}"
case "${CLASS}" in
  swap-off) SWAP_VALUE=0 ;;
  swap-on) SWAP_VALUE="${SWAP_ALLOWANCE:-67108864}" ;;
  *)
    echo "CLASS must be swap-off|swap-on, got '${CLASS}'" >&2
    exit 2
    ;;
esac

LADDER="${LADDER:-3 4 5 6}"
HOLD="${HOLD:-20s}"
WINDOW="${WINDOW:-60s}"
SAMPLE_SECONDS="${SAMPLE_SECONDS:-2}"
MEM_FLOOR_MIB="${MEM_FLOOR_MIB:-250}"
READY_TIMEOUT="${READY_TIMEOUT:-300}"
# Repeats per rung. A single sample cannot separate "held" from "got lucky" —
# identical configuration produced 12, 27 and 0 failures at the same rung.
REPEATS="${REPEATS:-1}"

# Two-layer compression (CONFIG_ZRAM_MULTI_COMP). The primary algorithm handles
# every page on its way into zram and therefore sits on the fault path; the
# secondary is applied by a recompression pass to pages already stored, and its
# CPU cost lands wherever that pass runs — not in any pod's cpu.stat.
#   RECOMP_ALGO  secondary algorithm, e.g. zstd. Empty = no pass at all.
#   RECOMP_TYPE  huge | idle | both — which stored pages are eligible.
#   RECOMP_WHEN  before (between rungs, quiet node) | during (mid-window)
RECOMP_ALGO="${RECOMP_ALGO:-}"
RECOMP_TYPE="${RECOMP_TYPE:-both}"
RECOMP_WHEN="${RECOMP_WHEN:-before}"
out_dir="${script_dir}/runs"
namespace="sandbox-demo"
pool="browser-pool"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
loop_pid=""

mkdir -p "${out_dir}"

# A report must not mix generations. results/ accumulates across plays, and an
# earlier run left `disk` and `zram` rows in the same table as today's arms —
# measured at a different hold, with 27 sessions where the new rungs have 18. Old
# artifacts for this class are archived, never merged.
archive="${out_dir}/prev-${CLASS}-$(date -u +%Y%m%dT%H%M%SZ)"
if compgen -G "${out_dir}/cap-${CLASS}-*.json" >/dev/null; then
  mkdir -p "${archive}"
  mv "${out_dir}"/cap-"${CLASS}"-*.json "${archive}/" 2>/dev/null || true
  mv "${out_dir}"/cap-"${CLASS}"-*.db "${archive}/" 2>/dev/null || true
fi

stop_loop() {
  if [[ -n "${loop_pid}" ]]; then
    kill -TERM "${loop_pid}" 2>/dev/null || true
    wait "${loop_pid}" 2>/dev/null || true
    loop_pid=""
  fi
}
trap stop_loop EXIT

api_ok() {
  timeout 15 ssh -F /dev/null -i "${SSH_KEY}" -p "${SSH_PORT}" \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -o ConnectTimeout=5 "${SSH_USER}@${VM_HOST}" \
    'sudo k3s kubectl get --raw /readyz' >/dev/null 2>&1
}

mem_available_mib() {
  vm_ssh "awk '/MemAvailable/{print int(\$2/1024)}' /proc/meminfo" 2>/dev/null | tr -d '\r' || echo 0
}

set_replicas() {
  vm_ssh "sudo k3s kubectl -n ${namespace} patch sandboxwarmpool ${pool} --type=merge -p '{\"spec\":{\"replicas\":$1}}'" >/dev/null
}

ready_replicas() {
  vm_ssh "sudo k3s kubectl -n ${namespace} get sandboxwarmpool ${pool} -o jsonpath='{.status.readyReplicas}'" 2>/dev/null | tr -d '\r'
}

# pool_diagnosis answers "why is the pool not ready" in one line, so a failed rung
# carries a cause instead of a zero.
pool_diagnosis() {
  local pods events
  pods="$(vm_ssh "sudo k3s kubectl -n ${namespace} get pods --no-headers 2>/dev/null | awk '{print \$2, \$3}' | sort | uniq -c | tr '\n' ' '" 2>/dev/null | tr -s ' ')"
  events="$(vm_ssh "sudo k3s kubectl -n ${namespace} get events --sort-by=.lastTimestamp 2>/dev/null | tail -3 | awk '{\$1=\"\";\$2=\"\";\$3=\"\";print}' | tr '\n' '|'" 2>/dev/null | tr -s ' ')"
  echo "pods[${pods:-none}] events[${events:-none}]"
}

row() {
  # class replicas rep held ok fail queue swap refaults | cpu ratio dev recomp_s recomp_cpu host_cpu | note
  # A failed rung has no measurement, so the six measurement columns are "-" and
  # the table keeps its shape.
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t-\t-\t-\t-\t-\t-\t%s\n' \
    "$1" "$2" "$3" "$4" "$5" "$6" "$7" "$8" "$9" "${10}"
}

# zram_state prints "orig_bytes compr_bytes mem_used_bytes real_size_bytes".
# Without it a ratio arm cannot be told apart from an arm whose device simply
# never filled: the rung-6 failure in the earlier run was the device running out
# of room, not the algorithm being wrong.
zram_state() {
  vm_ssh "awk '{print \$1, \$2, \$3}' /sys/block/zram0/mm_stat 2>/dev/null; cat /sys/block/zram0/disksize 2>/dev/null" 2>/dev/null |
    tr '\n' ' ' | tr -s ' ' | sed 's/ *$//'
}

# host_busy_jiffies is total non-idle CPU in the guest. Recompression runs in
# kernel context on behalf of the writing process, so no pod cgroup accounts for
# it — this is the only way the table can show what the pass cost.
host_busy_jiffies() {
  vm_ssh "awk '/^cpu /{print \$2+\$3+\$4+\$7+\$8+\$9}' /proc/stat" 2>/dev/null | tr -d '\r'
}

# recompress_pass runs the secondary compressor over already-stored pages and
# reports what it cost: wall time, CPU jiffies burned, and the device before and
# after. `both` does the size-based selection first (deterministic, no idle
# marking) and then the idle-marked one, because marking is explicit on this
# kernel (CONFIG_ZRAM_TRACK_ENTRY_ACTIME is off).
recompress_pass() {
  vm_ssh "sudo sh -s ${RECOMP_ALGO} ${RECOMP_TYPE}" <<'SH' | tr '\n' ' ' | tr -s ' ' | sed 's/ *$//'
set -e
algo="$1"; type="$2"
[ -e /sys/block/zram0/mm_stat ] || { echo "none none none none 0 0 0 0 0 no-device"; exit 0; }
read -r orig0 compr0 mem0 <<EOF
$(awk '{print $1, $2, $3}' /sys/block/zram0/mm_stat)
EOF
busy0=$(awk '/^cpu /{print $2+$3+$4+$7+$8+$9}' /proc/stat)
t0=$(date +%s%N)
# Availability must be checked against comp_algorithm, NOT recomp_algorithm:
# recomp_algorithm is empty until someone writes to it, so checking it silently
# skipped every pass and the multi arm measured as if it had no recompression.
avail="$(sed 's/[][]//g' /sys/block/zram0/comp_algorithm 2>/dev/null)"
reason=ok
ok=1
# Distinguish "this kernel has no multi-comp support at all" from "the write was
# rejected". 2026-11-04: every rung reported ok=0 and the reason was the only clue
# left, because the node is Ubuntu (not the Fedora host whose kernel does have
# CONFIG_ZRAM_MULTI_COMP). Without this branch both cases look identical.
if [ ! -e /sys/block/zram0/recomp_algorithm ]; then
  ok=0; reason="kernel has no recomp_algorithm (CONFIG_ZRAM_MULTI_COMP off)"
elif ! echo "${avail}" | tr ' ' '\n' | grep -qx "${algo}"; then
  ok=0; reason="algo ${algo} not compiled in"
elif [ "$(cat /sys/block/zram0/recomp_algorithm 2>/dev/null | sed 's/[][]//g' | tr -d ' ')" = "${algo}" ]; then
  : # already selected at boot, which is the only moment the kernel allows it
elif ! echo "${algo}" > /sys/block/zram0/recomp_algorithm 2>/dev/null; then
  # EBUSY: once disksize is set the device is initialised and the secondary
  # algorithm is frozen. Boot must have chosen it (setup-swap.sh does).
  ok=0; reason="recomp_algorithm is frozen on an initialised device (set __ZRAM_SECONDARY__ at boot; current: $(cat /sys/block/zram0/recomp_algorithm 2>/dev/null | tr ' ' ','))"
else
  case "${type}" in
    huge) echo "type=huge" > /sys/block/zram0/recompress 2>/dev/null || { ok=0; reason="type=huge refused"; } ;;
    idle) echo all > /sys/block/zram0/idle 2>/dev/null || true
          echo "type=idle" > /sys/block/zram0/recompress 2>/dev/null || { ok=0; reason="type=idle refused"; } ;;
    both) echo "type=huge" > /sys/block/zram0/recompress 2>/dev/null || { ok=0; reason="type=huge refused"; }
          echo all > /sys/block/zram0/idle 2>/dev/null || true
          echo "type=idle" > /sys/block/zram0/recompress 2>/dev/null || { ok=0; reason="type=idle refused"; } ;;
  esac
fi
t1=$(date +%s%N)
busy1=$(awk '/^cpu /{print $2+$3+$4+$7+$8+$9}' /proc/stat)
read -r orig1 compr1 mem1 <<EOF
$(awk '{print $1, $2, $3}' /sys/block/zram0/mm_stat)
EOF
echo "${orig0} ${compr0} ${orig1} ${compr1} $(( (t1-t0)/1000000 )) $(( busy1-busy0 )) ${ok} ${mem1} ${reason}"
SH
}

echo "== building session-lab for the guest =="
(
  cd "${repo_root}/apps/session-lab"
  GOOS=linux GOARCH=amd64 go build -o "${out_dir}/session-lab" ./cmd/session-lab
)
scp -F /dev/null -i "${SSH_KEY}" -P "${SSH_PORT}" -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  "${out_dir}/session-lab" "${SSH_USER}@${VM_HOST}:/home/${SSH_USER}/session-lab.new"
vm_ssh "mv -f /home/${SSH_USER}/session-lab.new /home/${SSH_USER}/session-lab && chmod 0755 /home/${SSH_USER}/session-lab"
vm_ssh "mkdir -p /home/${SSH_USER}/.kube && sudo cp /etc/rancher/k3s/k3s.yaml /home/${SSH_USER}/.kube/config \
  && sudo chown ${SSH_USER}:${SSH_USER} /home/${SSH_USER}/.kube/config && chmod 600 /home/${SSH_USER}/.kube/config && mkdir -p /home/${SSH_USER}/runs"

# wait_pool polls until the pool reports n ready sandboxes, or the budget expires.
wait_pool() {
  local n="$1" ready="" waited=0
  while [[ "${waited}" -lt "${READY_TIMEOUT}" ]]; do
    api_ok || return 2
    ready="$(ready_replicas)"
    [[ "${ready:-0}" -ge "${n}" ]] && return 0
    sleep 5
    waited=$((waited + 5))
  done
  return 1
}

one_point() {
  local n="$1"
  local rep="${2:-1}"
  local run_id="cap-${CLASS}-${n}-r${rep}-${stamp}"
  local avail

  avail="$(mem_available_mib)"
  if [[ "${avail:-0}" -lt "${MEM_FLOOR_MIB}" ]]; then
    row "${CLASS}" "${n}" "${rep}" "no" 0 0 0 0 0 "mem-available ${avail}MiB < ${MEM_FLOOR_MIB}"
    return 1
  fi

  # Fresh pods for this rung, so the class is set on the pods that will serve it.
  set_replicas 0
  sleep 8
  set_replicas "${n}"

  if ! wait_pool "${n}"; then
    local rc=$?
    if [[ "${rc}" -eq 2 ]]; then
      row "${CLASS}" "${n}" "${rep}" "no" 0 0 0 0 0 "api stopped answering while filling the pool"
    else
      row "${CLASS}" "${n}" "${rep}" "no" 0 0 0 0 0 \
        "pool reached $(ready_replicas)/${n} in ${READY_TIMEOUT}s — $(pool_diagnosis)"
    fi
    return 1
  fi

  # Now the pods exist: write the class, keep writing it while the load runs
  # (a refill mid-run would otherwise come up with kubelet's own allowance), and
  # refuse to measure if the kernel did not take the value.
  "${script_dir}/swap-limit.sh" "${SWAP_VALUE}" >/dev/null
  if [[ "${SWAP_VALUE}" == "0" ]]; then
    "${script_dir}/verify-swap.sh" 0 >/dev/null || {
      row "${CLASS}" "${n}" "${rep}" "no" 0 0 0 0 0 "memory.swap.max is not 0 on the browser containers"
      return 1
    }
  else
    "${script_dir}/verify-swap.sh" nonzero >/dev/null || {
      row "${CLASS}" "${n}" "${rep}" "no" 0 0 0 0 0 "memory.swap.max is still 0 on the browser containers"
      return 1
    }
  fi

  ( while sleep 5; do "${script_dir}/swap-limit.sh" "${SWAP_VALUE}" >/dev/null 2>&1 || true; done ) &
  loop_pid=$!

  # Two-layer compression: densify what is already stored while the node is
  # quiet. Recorded before the window so the recompression CPU is attributed
  # here rather than hiding inside the load's own numbers.
  local recomp_ms=0 recomp_cpu=0 recomp_ok=0 recomp_reason="" zr_before zr_after
  zr_before="$(zram_state)"
  if [[ -n "${RECOMP_ALGO}" && "${RECOMP_WHEN}" == "before" ]]; then
    read -r _ _ _ _ recomp_ms recomp_cpu recomp_ok _ recomp_reason <<<"$(recompress_pass)"
  fi
  zr_after="$(zram_state)"
  local busy0 busy1
  busy0="$(host_busy_jiffies)"

  local rate
  # Offered load must equal the rung: concurrency = rate × hold. This was hardcoded
  # to /20 while HOLD became tunable, so with hold 30s every rung ran 1.5× over its
  # replica count — which made those arms incomparable with the ones run at 20s and
  # turned "survived a rung" into a measure of overload tolerance.
  local hold_s
  hold_s="$(python3 -c "
import re
m = re.match(r'^(\d+)(s|m|h)?$', '${HOLD}')
n = int(m.group(1)); unit = m.group(2) or 's'
print(n * {'s':1,'m':60,'h':3600}[unit])
")"
  rate="$(python3 -c "print(${n}/${hold_s})")"

  # RECOMP_WHEN=during: fire the pass inside the window, at the halfway point, so
  # the burst lands on a node that is already loaded. Separate subprocess: its
  # CPU shows up in host_cpu below, not in any pod's cpu.stat.
  if [[ -n "${RECOMP_ALGO}" && "${RECOMP_WHEN}" == "during" ]]; then
    (
      sleep $(( $(python3 -c "print(int(${hold_s}/2))") ))
      recompress_pass >"${out_dir}/${run_id}.recomp-during.txt" 2>/dev/null || true
    ) &
  fi

  local ran=0
  if vm_ssh "cd /home/${SSH_USER} && ./session-lab --namespace ${namespace} --pool ${pool} \
      --policy destroy --duration ${WINDOW} --rate ${rate} --hold ${HOLD} \
      --pool-replicas ${n} --sample-seconds ${SAMPLE_SECONDS} \
      --kubeconfig /home/${SSH_USER}/.kube/config \
      --out /home/${SSH_USER}/runs --run-id ${run_id}" >/dev/null 2>&1; then
    ran=1
  fi
  busy1="$(host_busy_jiffies)"
  stop_loop

  if [[ "${ran}" -ne 1 ]]; then
    row "${CLASS}" "${n}" "${rep}" "no" 0 0 0 0 0 "session-lab failed (api or node unavailable)"
    return 1
  fi

  scp -F /dev/null -i "${SSH_KEY}" -P "${SSH_PORT}" -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    "${SSH_USER}@${VM_HOST}:/home/${SSH_USER}/runs/${run_id}.json" "${out_dir}/${run_id}.json" >/dev/null 2>&1 || {
    row "${CLASS}" "${n}" "${rep}" "no" 0 0 0 0 0 "no report produced"
    return 1
  }

  python3 - "${out_dir}/${run_id}.json" "${CLASS}" "${n}" "${RECOMP_ALGO:-none}" \
    "${recomp_ms}" "${recomp_cpu}" "${recomp_ok}" "${zr_before}" "${zr_after}" \
    "$(( (busy1 - busy0) / 100 ))" "${out_dir}/${run_id}.zram.json" "${recomp_reason:-}" \
    "${rep}" <<'PY'
import json, sys
report = json.load(open(sys.argv[1]))
cls, n = sys.argv[2], sys.argv[3]
recomp_algo = sys.argv[4]
recomp_ms, recomp_cpu, recomp_ok = int(sys.argv[5]), int(sys.argv[6]), int(sys.argv[7])
zr_before, zr_after = sys.argv[8].split(), sys.argv[9].split()
host_cpu_s = int(sys.argv[10])
sidecar_path = sys.argv[11]
reason = sys.argv[12] if len(sys.argv) > 12 else ""
rep = sys.argv[13] if len(sys.argv) > 13 else "1"
recomp_type = None

res = report.get("resources", {})
outcomes = report.get("outcomes", {})
ok = outcomes.get("success", 0)
bad = sum(v for k, v in outcomes.items() if k != "success")
mins = (report.get("ended_ns", 0) - report.get("started_ns", 0)) / 60e9
q = (report.get("queue_wait_ms") or {}).get("p95", 0)
swap = res.get("swap_peak_max_mib", 0) or 0
oom = res.get("oom_kills", 0)
refaults = res.get("refault_sessions", 0)
cpu = res.get("cpu_core_seconds_p50", 0)

# mm_stat: orig_data_size compr_data_size mem_used_total [disksize]
def ratio(orig, compr):
    try:
        o, c = float(orig), float(compr)
        return o / c if c else 0.0
    except (TypeError, ValueError):
        return 0.0

def num(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return 0.0

orig_after = zr_after[0] if len(zr_after) > 0 else 0
compr_after = zr_after[1] if len(zr_after) > 1 else 0
mem_after = zr_after[2] if len(zr_after) > 2 else 0
disksize = zr_after[3] if len(zr_after) > 3 else 0
orig_before = zr_before[0] if len(zr_before) > 0 else 0
compr_before = zr_before[1] if len(zr_before) > 1 else 0
# dev_pct is how full the device got: the earlier rung-6 failure was the device
# running out of room, not the algorithm being wrong.
dev_pct = (num(orig_after) / num(disksize) * 100) if num(disksize) else 0.0
# mm_stat is cumulative over the device's lifetime, so the raw ratio mixes every
# rung that came before. The rung's own density is the ratio of the deltas; the
# cumulative figure is kept as a fallback for the first rung (no delta yet).
d_orig = num(orig_after) - num(orig_before)
d_compr = num(compr_after) - num(compr_before)
r_rung = (d_orig / d_compr) if d_compr > 0 else 0.0
r_after = ratio(orig_after, compr_after)
r_before = ratio(zr_before[0], zr_before[1]) if len(zr_before) > 1 else 0.0
ratio_reported = r_rung or r_after

side = {
    "class": cls, "replicas": n, "recomp_algo": recomp_algo,
    "recomp_type": recomp_type, "recomp_ok": recomp_ok, "recomp_reason": reason,
    "recomp_ms": recomp_ms, "recomp_cpu_s": round(recomp_cpu / 100.0, 2),
    "host_cpu_s": host_cpu_s or 0,
    "ratio_before": round(r_before, 2), "ratio_after": round(r_after, 2),
    "ratio_rung": round(r_rung, 2), "ratio_reported": round(ratio_reported, 2),
    "orig_after_bytes": int(num(orig_after)), "compr_after_bytes": int(num(compr_after)),
    "mem_used_bytes": int(num(mem_after)), "disksize_bytes": int(num(disksize)),
    "dev_used_pct": round(dev_pct, 1),
}
json.dump(side, open(sidecar_path, "w"), indent=2)

note = "ok" if bad == 0 else f"{bad} failed"
if recomp_algo and recomp_algo != "none" and recomp_ok != 1:
    note += f"; recompression FAILED: {reason or 'unknown'}"
print("\t".join(map(str, [cls, n, rep, "yes" if bad == 0 else "no", ok, bad,
                          f"{q:.0f}", f"{swap:.1f}", f"{refaults}/{res.get('sessions_with_samples', 0)}",
                          f"{cpu:.2f}", f"{ratio_reported:.2f}", f"{dev_pct:.0f}",
                          f"{recomp_ms/1000:.1f}", f"{recomp_cpu/100.0:.1f}", f"{host_cpu_s}",
                          f"{note}; {ok/min(1,mins):.1f}/min; oom={oom}"])))
sys.exit(0 if bad == 0 else 1)
PY
}

printf 'class\treplicas\trep\theld\tsuccess\tfailed\tqueue_p95_ms\tswap_peak_mib\trefaults\tcpu_core_s\tzram_ratio\tdev_used_pct\trecomp_s\trecomp_cpu_s\thost_cpu_s\tnote\n'

# A swap-on arm on a guest with no swap device cannot hold anything, and running it
# anyway hides the cause: the 2026-11-04 arm produced 21 failures with
# "Swap: 0 0 0" and "Mem available 22 MiB" while the table only showed failures.
# The swap device is a precondition, so it is checked as one.
if [[ "${CLASS}" == "swap-on" ]]; then
  swaps="$(vm_ssh 'grep -c . /proc/swaps 2>/dev/null || echo 0' || echo 0)"
  # /proc/swaps has a header line, so a real device means 2+
  if [[ "${swaps}" -lt 2 ]]; then
    echo "guest has no active swap device (${swaps} line(s) in /proc/swaps): swap-on cannot be measured." >&2
    vm_ssh 'cat /proc/swaps; free -m' >&2 || true
    row "${CLASS}" "-" "-" "no" 0 0 0 0 0 "guest has no active swap device; arm not measurable"
    exit 1
  fi
  echo "guest swap:"
  vm_ssh 'cat /proc/swaps' || true
fi

# A recompression arm on a guest that cannot recompress is not a slow arm, it is a
# duplicate of the control arm. Measured on Ubuntu 24.04: six rungs of
# zram-multi ran byte-identical to zram-multi-ctl and the only trace was a reason
# string per row. Refuse up front instead — 3 seconds, not 35 minutes.
if [[ -n "${RECOMP_ALGO}" ]]; then
  probe="$(vm_ssh 'if [ -e /sys/block/zram0/recomp_algorithm ]; then
      echo "have:$(sed s/[][]//g /sys/block/zram0/comp_algorithm)";
    else echo "missing"; fi' || true)"
  case "${probe}" in
    missing)
      echo "guest kernel has no /sys/block/zram0/recomp_algorithm: CONFIG_ZRAM_MULTI_COMP is off." >&2
      echo "Recompression cannot run, so this arm would duplicate the control arm. Stopping." >&2
      echo "Ubuntu ships it off (noble 6.8, plucky 6.14 annotations: amd64 'n'); Fedora 44 has it =y." >&2
      row "${CLASS}" "-" "-" "no" 0 0 0 0 0 "guest has no CONFIG_ZRAM_MULTI_COMP; arm not measurable"
      exit 1
      ;;
    have:*)
      if ! echo "${probe#have:}" | tr ' ' '\n' | grep -qx "${RECOMP_ALGO}"; then
        echo "guest has multi-comp but not ${RECOMP_ALGO}: ${probe#have:}" >&2
        row "${CLASS}" "-" "-" "no" 0 0 0 0 0 "secondary algo ${RECOMP_ALGO} not compiled in"
        exit 1
      fi
      echo "recompression available: ${RECOMP_ALGO} (secondary) on this guest"
      ;;
    *)
      echo "could not probe zram multi-comp support; continuing" >&2
      ;;
  esac
fi

for n in ${LADDER}; do
  for rep in $(seq 1 "${REPEATS}"); do
    # A rung that genuinely cannot hold is not repeated: the ceiling is a property
    # of the box, and three identical failures cost 6 minutes for one fact.
    if ! one_point "${n}" "${rep}"; then
      echo "  ceiling for ${CLASS}: ${n} did not hold (rep ${rep}/${REPEATS})" >&2
      break 2
    fi
  done
done

echo
echo "reports in ${out_dir} (cap-${CLASS}-*-${stamp}.json)"
