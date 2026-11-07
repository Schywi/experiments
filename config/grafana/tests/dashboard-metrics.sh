#!/usr/bin/env bash
# Every session_lab_* series the dashboard asks for must exist in the code.
#
# The failure this prevents: a panel whose query names a metric the runner never
# exports. That reads as an empty panel, which is indistinguishable from a run that
# simply had nothing to report, and the dashboard cannot say which of the two it is.
#
#   bash config/grafana/tests/dashboard-metrics.sh
#
# It also checks the reverse for the runner's own series: a metric the code exports
# with no panel is a number nobody is looking at.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../../.." && pwd)"
dashboard="${repo_root}/config/grafana/dashboards/session-lab.json"
metrics_go=(
  "${repo_root}/apps/session-lab/internal/platform/metrics/metrics.go"
  "${repo_root}/apps/session-lab/internal/infra/pool/metrics.go"
)

status=0

# Names the dashboard queries, without the _bucket/_count/_sum suffixes.
used="$(jq -r '[.panels[].targets[]?.expr] | .[] | scan("session_lab_[a-z_]+")' "${dashboard}" |
  sed -E 's/_(bucket|count|sum)$//' | sort -u)"

# Names the Go code registers.
defined="$(grep -ho 'Name: *"[a-z_]*"' "${metrics_go[@]}" | sed 's/.*Name: *"//; s/"//' | sort -u)"

while read -r name; do
  [[ -n "${name}" ]] || continue
  if ! grep -qx "${name}" <<<"${defined}"; then
    echo "dashboard queries ${name}, which no code registers" >&2
    status=1
  fi
done <<<"${used}"

# Series that exist but are not on the dashboard. Boundaries and the pool gauges are
# covered by panels under other names, so only the runner's own metrics are listed.
while read -r name; do
  [[ -n "${name}" ]] || continue
  if ! grep -qx "${name}" <<<"${used}"; then
    echo "no panel for ${name}" >&2
    status=1
  fi
done < <(grep -qx 'session_lab_sessions_total' <<<"${defined}" &&
  printf '%s\n' \
    session_lab_sessions_active session_lab_sessions_waiting \
    session_lab_sessions_total session_lab_queue_wait_seconds \
    session_lab_acquire_seconds session_lab_hold_seconds \
    session_lab_reset_seconds session_lab_release_seconds session_lab_resume_seconds \
    session_lab_cpu_seconds_total session_lab_throttled_seconds_total \
    session_lab_workingset_peak_bytes session_lab_boundaries_total \
    session_lab_template_cpu_limit_cores session_lab_session_task_seconds \
    session_lab_session_js_heap_bytes session_lab_instances_booting \
    session_lab_instance_booting_cores session_lab_boot_seconds \
    session_lab_node_cpu_cores session_lab_oom_kills_total |
  while read -r name; do grep -qx "${name}" <<<"${defined}" && echo "${name}"; done)

if ((status == 0)); then
  echo "dashboard series: $(wc -l <<<"${used}") queried, all registered in code"
fi
exit "${status}"
