# Production hosting on GKE or GCE — what cannot work, and what must change

> Status: **design note, not implemented.** Nothing in `config/` has been changed
> by this document. It exists so that "put this on Google Cloud" does not start
> by surprise-debugging a LoadBalancer that never gets an IP.

The short version: **Cilium LB IPAM and L2 announcements are a LAN feature.** They
work here because the node sits on a real L2 segment and can answer ARP/NDP for
`192.168.0.240-250`. Neither Google Compute Engine nor Google Kubernetes Engine
gives you that segment, and GKE does not let you own Cilium at all.

---

## 1. What breaks, and why

| Assumption in this repo | On GCE (self-managed k3s) | On GKE (managed) |
|---|---|---|
| `CiliumLoadBalancerIPPool` (`lan-pool`) hands out `192.168.0.240-250` | The VPC is **routed**, not one L2 segment. A node cannot ARP/NDP for an address GCP does not own, so pool IPs never become reachable. | Cilium is managed by the control plane (**Dataplane V2**). Installing our own Cilium operator and LB IPAM CRDs is not part of the contract. |
| `io.cilium/lb-ipam-ips` annotations + `loadBalancerIP:` pins | Silently useless: the address is never assigned, and a pinned-but-unassignable IP can leave the Service pending. | Same, and additionally meaningless — the cloud controller, not Cilium, fulfils `type: LoadBalancer`. |
| `CiliumL2AnnouncementPolicy` | No L2 to announce on. | Not available. |
| Ingress served by the Cilium ingress controller as a `LoadBalancer` | Replace with **NodePort 30080** behind a VPC firewall/Cloudflare, or a real GCP LB. | Use **GKE Ingress / Gateway API**, or `Service type=LoadBalancer` provisioned by GKE. |
| `k8sServiceHost: 127.0.0.1` (the agent runs in the host netns) | Must become the **instance's internal IP** (metadata server), or pods lose the API and therefore DNS. | Not ours to set. |
| `write-kubeconfig-mode: "0644"` | Must be **`0600`** on a host with a public interface. | Managed control plane; kubeconfig comes from `gcloud`. |
| Bare-IP UI links (`http://192.168.0.24x/`) baked into the assistant | Must become ingress hostnames + TLS. | Same. |
| `/dev/dri` + `amd.com/render` device plugin | No GPU on the reference VM. | No AMD card; cloud GPUs are **CUDA-only** (T4/L4/A100). |
| Local registry (`config/registry/`, Kaniko) | Fine on a single VM, but pushes over the network from your laptop. | Use **Artifact Registry** + image pull secrets. |
| host firewalld trusting pod/service CIDRs | No host firewall: the **VPC firewall** owns ingress, and 6443 stays closed. | GCP firewall + GKE network policy. |

### Every place in this repository that assumes a pinned LAN IP

These are the files to make profile-scoped (not to delete):

| Path | What it pins |
|---|---|
| `config/k3s/cilium-lb-ipam.yaml` | the `192.168.0.240-250` pool + L2 announcement policy |
| `config/argocd/argocd-service.yaml` | `io.cilium/lb-ipam-ips: 192.168.0.247` |
| `config/grafana/values.yaml`, `templates/service.yaml` | `192.168.0.245` (+ CNP allowing the LB IP) |
| `config/victoriametrics/values.yaml`, `templates/service.yaml` | `192.168.0.246` (+ CNP) |
| `config/cartography/values.yaml`, `templates/neo4j.yaml` | `192.168.0.242`, bolt `7687`, Browser CSP with the IP |
| `apps/assistant/chart/values.yaml`, `templates/service.yaml` | `192.168.0.243` + UI deep links to `.241/.242/.245/.246` |
| `config/k3s/config.yaml` | `write-kubeconfig-mode: "0644"` (must be `0600` off-LAN) |
| `scripts/hosts-entries.sh` | the `.localhost` / `/etc/hosts` fallback |

---

## 2. Exposure matrix

| Concern | LAN host (today) | GCE, self-managed k3s | GKE, managed |
|---|---|---|---|
| L4 exposure | Cilium LB IPAM + L2 ARP | NodePort 30080 behind VPC firewall, or GCP forwarding rules | GKE `LoadBalancer` Service / Gateway |
| Ingress | Cilium ingress controller (`class: cilium`) | Cilium ingress as **NodePort**, or GKE-less: Cloudflare Tunnel | GKE Ingress / Gateway API |
| Address stability | pinned pool IPs | ephemeral external IP → DDNS | reserved static IP |
| TLS | none (lab) | Cloudflare edge | GKE-managed cert / Cloudflare |
| Kubeconfig mode | `0644` | `0600` | managed |
| API exposure | LAN `6443` (firewalld) | **never opened** — SSH only | private cluster / authorized networks |
| GPU | RX 570, Vulkan, `amd.com/render` | none (or CUDA L4/T4 + NVIDIA plugin) | CUDA L4/T4 + NVIDIA plugin |
| Images | in-cluster registry + Kaniko | same, over the network | Artifact Registry |

A legitimate middle path on GCE: keep Cilium and use its **BGP control plane**
peered with Cloud Router, which produces real routable LB addresses instead of
L2 announcements. That is more moving parts than NodePort + Cloudflare and has
not been tried here — treat it as an option, not a recommendation.

---

## 3. What the sibling `gce` Terraform lab already solved

There is a separate, self-contained Terraform root module (sibling repository,
not part of this one) that stands up a single Spot VM running this k3s + Cilium
platform. It is the reference for the deployed profile; the parts worth copying:

**Base + per-environment overlay, so profiles cannot drift.** One shared
`platform/base/` (`k3s-config.yaml`, `cilium-values.yaml`) plus tiny
`platform/envs/<env>/` overlays (`local`, `gce`), merged by `install.sh`:

- **k3s config is concatenated** (`base` then overlay) into
  `/etc/rancher/k3s/config.yaml`. An overlay may only **add** keys — redefining a
  `base` key produces a duplicate and k3s refuses to start, which is a loud
  failure instead of silent divergence.
- **Cilium values use Helm layering** (`helm -f base -f env`), so an overlay
  *overrides*; `k8sServiceHost`/`k8sServicePort` are passed with `--set` because
  they are computed per boot.
- Adding an environment is one directory with only the differing files.

**The `gce` overlay is exactly the delta from section 1**: `APPLY_LB_IPAM=0`,
`EXPOSURE=NodePort`, `K8S_SERVICE_HOST=auto` (internal IP from the metadata
server), `write-kubeconfig-mode: "0600"`, `HOST_FIREWALL=none` (VPC owns
ingress), Cloudflare DDNS on every boot because there is no static IP.

**Cost and blast-radius controls:** Spot with
`instance_termination_action = STOP`, a business-hours schedule, and a billing
budget that publishes to Pub/Sub so a Cloud Function stops every running
instance when spend crosses a threshold. Stopping keeps the disk and stops VM
billing; the budget is a guardrail, not a hard cap.

**Security hygiene:** 6443 never opened; SSH restricted by source range (the
default `0.0.0.0/0` is called out as unsafe); CI runs `terraform fmt -check`,
`validate`, `tflint`, `gitleaks`, Trivy, Checkov and CodeQL, with Terratest
manual-only and authenticated by Workload Identity Federation instead of stored
keys.

---

## 4. Migration checklist for this repository

1. **Adopt the overlay model**, not a second copy: e.g.
   `config/profiles/{lan,gce,gke}/` with the shared k3s/Cilium base, or vendor the
   sibling lab's `platform/base` + `platform/envs` shape.
2. **Move every pinned IP out of the base** — the table in §1 becomes overlay-only
   (or is deleted for cloud profiles). Do not leave `io.cilium/lb-ipam-ips`
   annotations behind in a profile where Cilium does not own LBs.
3. **Rewrite the exposure path**: NodePort/Wake-up tunnel on GCE, GKE Ingress on
   GKE. Update the assistant's UI `urls` to hostnames, since bare IPs will not
   survive a stop/start cycle.
4. **Harden the profile deltas**: `write-kubeconfig-mode: "0600"`, 6443 closed at
   the network layer, no `NEO4J_AUTH=none`, Grafana anonymous `Viewer` instead of
   Admin, Argo CD stays anonymous **read-only** (`policy.default:
   role:readonly`) and never `server.disable.auth`.
5. **Repoint the CNPs** that allow LB IPs to the new exposure path, or they will
   quietly block the only way in.
6. **Swap the image pipeline** for Artifact Registry (GKE) or keep the local
   registry on a single GCE VM.
7. **GPU: do not port Track C.** The Vulkan/`amd.com/render` work is
   AMD-Polaris-specific. Cloud GPUs are CUDA-only and need the NVIDIA device
   plugin; the honest statement is "Track C does not transfer", and the models
   fall back to CPU as they do today.
8. **Keep Argo CD contextual.** `targetRevision` keeps pointing at the branch
   whose work lives there; a cloud profile is a new overlay, not a new branch per
   host.

---

## 5. What does not change

Cilium as the only CNI (`flannel-backend: none`, no kube-proxy, no Traefik/
ServiceLB), the pod/service CIDRs, the CPU models, the assistant and its
read-only RBAC, the Worm lab, the benchmark methodology, and the repository
layout. Cloud hosting changes **exposure and identity**, not the platform's
shape.

---

## 6. Honest limits of this note

- Nothing here has been executed against a cluster.
- GKE's managed Cilium behaviour is described from its documented contract
  (Dataplane V2), not measured in this lab.
- The sibling lab targets GCE only: no GKE, no multi-node, no GPU, single zone.
- A separate, gitignored VPS/Cloudflare-Tunnel plan exists under `docs/`; it
  covers a different host (DigitalOcean) and is deliberately not referenced as a
  source of truth here.
