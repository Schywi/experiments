# Phase 2 — Native k3s on the Fedora host, Track A (2026-10-06)

**Branch:** `native-k3s-gpu-models` · anchors `9059891` (plan) → `a2a7a41`
(platform profile) → `4368ef9` → `cee5c95` (exposure stack) → `a8cd95d`
(observability) · **tracked doc:** [`../config/k3s/README.md`](../config/k3s/README.md)

## The decision

Move the identical component set off Docker-in-Docker and onto the real host:
same Cilium version, same ingress, same Argo CD posture — but now on a kernel
that actually implements what Cilium assumes. The plan and its rejected
alternatives live in
[`../research/native-k3s-gpu-models-plan.md`](../research/native-k3s-gpu-models-plan.md).

## What changed structurally

| Concern | k3d (Phase 1) | Native k3s (Phase 2) | Why |
|---|---|---|---|
| Runtime | k3s inside a Docker container, 2 GiB | k3s as a systemd service on Fedora 44 | real kernel → eBPF |
| CNI | Cilium + iptables masquerade fallback | Cilium with kube-proxy replacement, no fallback | the fallback was hiding the datapath |
| LoadBalancer | MetalLB built from source into the node | **Cilium LB-IPAM + L2 announcements** (`config/k3s/cilium-lb-ipam.yaml`) | one fewer component, ARP on the real LAN |
| Addresses | `172.20.0.x` Docker bridge | `192.168.0.240-250` on the LAN, node `.28` | reachable from the LAN, not just the host |
| Image delivery | `k3d image import` | in-cluster registry + `k3s ctr` / Kaniko (`config/registry/`) | no root, no Docker daemon |
| DNS | node `resolv.conf` patched in the k3d node | cluster-owned `resolv-conf` (`config/k3s/resolv.conf`) | host `search mygateway` leaked into pods and stalled external lookups |
| Secrets | none | Sealed Secrets (`config/sealed-secrets/`) | commit-safe secrets for the graph stack |

Two configuration traps are documented **in** `config/k3s/config.yaml` because
they cost real debugging time and look like application bugs:

- **`bind-address` must stay unset.** k3s advertises the *node IP* as the
  `kubernetes` endpoint; binding the API to loopback leaves every pod unable to
  reach the API — and therefore unable to resolve DNS — while the node still
  reports `Ready` (`4368ef9`).
- **`fail-swap-on=false`** is required on a zram host, separate from the pod
  swap decision (`memorySwap.swapBehavior`).

## Why not a VM — the GPU argument

A VM was the obvious middle path: keep the k3d conveniences, get systemd and a
real kernel, and stay sandboxed. It was rejected, and the reason is GPU
*ownership* rather than performance.

`/dev/dri/renderD128` is bound to the host's `amdgpu` driver and is the node the
Track C device plugin hands to pods. A VM with GPU passthrough rebinds that
device to `vfio-pci` **on the host**, which removes it from the host session:
no display, no Vulkan, and it does not come back until the binding is undone and
the machine reboots. On a single-GPU desktop — where `card1` at PCI
`0000:01:00.0` is the card driving the monitors — that is indistinguishable from
losing the hardware, for the sake of an experiment that needs the card anyway.

So the pivot went **past** the VM, straight to the host: `k3s` as a systemd
service, cluster networking on the real kernel, and the GPU still owned by the
desktop. `AGENTS.md` turns this into a hard prohibition so no future agent
re-introduces a VM "just to test".

## Platform services added in this phase

- **Argo CD** with one Application per workload, each reading the branch its
  work lives on — the "Applications are contextual, never `main`" rule
  (`954fbc9`, later codified in `AGENTS.md`). Repo-server probe timeouts were
  raised for slow chart rendering (`66df612`).
- **Cartography** — Neo4j plus a scheduled Kubernetes sync every 6 hours,
  credentials sealed (`dbcc855`). This is the graph Phase 5 queries.
- **Observability** — VictoriaMetrics scraping the Cilium agent/operator and
  the Hubble agent/relay, with the official Cilium dashboards in Grafana
  (`a8cd95d`).
- **Rootless image delivery** — buildah-built images reach k3s through the
  in-cluster registry instead of a root-owned containerd import (`fc3e35b`).

## What it proved

The same experiment runs unmodified on real hardware, and the parts that had
been faked (BPF SNAT, host-legacy routing, brute-force image import) simply
disappear. It also proved the negative: **most of the k3d troubleshooting
surface was substrate, not design** — so those fixes must not be carried
forward as if they were requirements.

## What it left behind

`config/k3d/`, `config/metallb/`, `start.sh` and `Tiltfile*` still exist and
still work on a Docker host, but they are Phase-1 artifacts. They are documented
as such in [`../ROADMAP.md`](../ROADMAP.md) §4 rather than deleted, because the
k3d profile is the cheapest way to reproduce Phase 1 without hardware.
