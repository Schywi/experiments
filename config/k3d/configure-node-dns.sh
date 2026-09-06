#!/usr/bin/env bash

set -euo pipefail

command -v docker >/dev/null 2>&1 || {
  echo "docker is required" >&2
  exit 1
}

CLUSTER_NAME="${K3D_CLUSTER_NAME:-cilium-lab}"
DNS_SERVERS="${K3D_DNS_SERVERS:-1.1.1.1,1.0.0.1}"

IFS=',' read -r -a dns_servers <<<"${DNS_SERVERS}"
if ((${#dns_servers[@]} == 0)); then
  echo "K3D_DNS_SERVERS must contain at least one IPv4 resolver" >&2
  exit 1
fi

for dns_server in "${dns_servers[@]}"; do
  if [[ ! "${dns_server}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    echo "K3D_DNS_SERVERS contains an invalid IPv4 resolver: ${dns_server}" >&2
    exit 1
  fi

  IFS='.' read -r -a octets <<<"${dns_server}"
  for octet in "${octets[@]}"; do
    if ((10#${octet} > 255)); then
      echo "K3D_DNS_SERVERS contains an invalid IPv4 resolver: ${dns_server}" >&2
      exit 1
    fi
  done
done

mapfile -t node_containers < <(
  docker ps \
    --filter "label=k3d.cluster=${CLUSTER_NAME}" \
    --filter 'label=k3d.role=server' \
    --format '{{.Names}}'
  docker ps \
    --filter "label=k3d.cluster=${CLUSTER_NAME}" \
    --filter 'label=k3d.role=agent' \
    --format '{{.Names}}'
)

if ((${#node_containers[@]} == 0)); then
  echo "no running k3d nodes found for cluster '${CLUSTER_NAME}'" >&2
  exit 1
fi

for node_container in "${node_containers[@]}"; do
  {
    printf 'nameserver %s\n' "${dns_servers[@]}"
    printf 'options ndots:0\n'
  } | docker exec -i "${node_container}" sh -c 'cat > /etc/resolv.conf'
  docker exec "${node_container}" nslookup registry-1.docker.io >/dev/null
  echo "Configured and verified DNS in ${node_container}"
done
