#!/usr/bin/env bash
# Print (or apply) the /etc/hosts entries for the local platform hostnames.
#
# Browsers resolve *.localhost to loopback, so the Cilium Ingress hostnames
# (argocd.localhost) and the Cartography/Neo4j hostnames must be mapped to the
# cluster's load-balancer IPs. Without --apply the script only prints the lines.
set -euo pipefail

# Fallbacks matching config/helm/cilium/values.yaml (shared ingress) and
# config/cartography/values.yaml (neo4j.loadBalancerIP).
default_ingress_ip="192.168.0.240"
default_cartography_ip="192.168.0.242"

# Prefer live cluster addresses when kubectl is available.
discovered_ingress=""
discovered_cartography=""
if command -v kubectl >/dev/null 2>&1; then
  discovered_ingress="$(
    kubectl -n kube-system get service cilium-ingress \
      -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || true
  )"
  discovered_cartography="$(
    kubectl -n cartography get service cartography-neo4j \
      -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || true
  )"
fi

ingress_ip="${INGRESS_IP:-${discovered_ingress:-${default_ingress_ip}}}"
cartography_ip="${CARTOGRAPHY_LB_IP:-${discovered_cartography:-${default_cartography_ip}}}"

# argocd.localhost is served by the shared Cilium Ingress; the Cartography/Neo4j
# names point at the Neo4j LoadBalancer so both the Browser (port 80) and bolt
# (7687) work on one hostname.
mapfile -t entries <<EOF
${ingress_ip} argocd.localhost
${cartography_ip} neo4j.localhost cartography.localhost
EOF

if [[ "${1:-}" != "--apply" ]]; then
  echo "# Add these lines to /etc/hosts (or run: ${0##*/} --apply):"
  printf '%s\n' "${entries[@]}"
  exit 0
fi

command -v sudo >/dev/null 2>&1 || {
  echo "sudo is required to edit /etc/hosts" >&2
  exit 1
}

for entry in "${entries[@]}"; do
  for host in ${entry#* }; do
    if grep -qE "[[:space:]]${host}([[:space:]]|$)" /etc/hosts; then
      echo "already present: ${host}"
      continue
    fi
    echo "${entry}" | sudo tee -a /etc/hosts >/dev/null
    echo "added: ${entry}"
    break
  done
done
echo "/etc/hosts updated"
