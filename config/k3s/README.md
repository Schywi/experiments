# Native k3s platform (Fedora host)

This directory is the **bare-metal counterpart** to `config/k3d/`. It installs
k3s directly on the Fedora host and lets Cilium own cluster networking, so the
host's GPU device nodes (`/dev/dri`) are within reach of Pods — which the Docker
sandbox of the k3d profile cannot provide.

The k3d profile is unchanged and still lives in `config/k3d/`.

```text
config/k3s/
├── config.yaml           # k3s server config → /etc/rancher/k3s/config.yaml
├── install-k3s.sh        # install/refresh native k3s (requires sudo)
├── cilium-values.yaml    # Cilium + Hubble values for a real host kernel
├── install-cilium.sh     # install Cilium + Hubble
├── bootstrap.sh          # sequential: k3s → Cilium → Argo CD → validate
├── delete.sh             # remove native k3s (requires sudo)
└── validate.sh           # node, Cilium, Hubble, Argo CD, and a net smoke Pod
```

## Why native k3s (and not k3d)

Two facts forced this path on this machine:

1. **k3d had Cilium problems here.** The Docker-backed k3d host kernel rejects
   Cilium 1.20's BPF SNAT program, so the k3d profile had to fall back to
   iptables masquerading (`bpf.masquerade: false`,
   `bpf.hostLegacyRouting: true`) and BPF TProxy disabled.
2. **The base Linux environment lacked `systemd` and eBPF.** k3s needs systemd
   to run as a service, and Cilium's datapath needs a working eBPF stack. The
   Fedora 44 host provides both (kernel 6.19, systemd, cgroup v2), so native
   k3s gets the full eBPF datapath back.

## System this was designed on

| Item | Value |
|---|---|
| OS | Fedora 44 Workstation, x86_64 |
| Kernel | 6.19.x (systemd + cgroup v2 + eBPF) |
| CPU | Intel i5-9400F (6C/6T, AVX2) |
| RAM | 15 GiB, with Fedora zram swap |
| GPU | AMD Radeon RX 570 (Polaris/gfx803) — `amdgpu` + Mesa RADV |
| SELinux | Disabled (simplifies `hostPath` device mounts) |

## Why `/dev/dri` (GPU access), and why the GPU is mounted at all

The RX 570 is **old and unsupported for modern compute**: it is Polaris
(`gfx803`), removed from AMD's supported ROCm matrix. There is no ROCm, no CUDA,
and no `amd.com/gpu` compute path on this card.

| Node | Enables | On this card |
|---|---|---|
| `/dev/dri/renderD128` | graphics, **Vulkan**, video (Mesa RADV / VA-API) | ✅ |
| `/dev/kfd` | AMD **compute** (ROCm) | ❌ unsupported |

Pods therefore mount **only `/dev/dri`**, and only to run Vulkan/graphics
workloads (the one class of GPU work the card still does). `/dev/kfd` is not
mounted. The GPU is mounted because it is the only way to validate the
kubelet → device → Pod path on hardware that has no compute stack, not to run
ROCm/PyTorch. See `research/native-k3s-gpu-models-plan.md` for the full plan.

## Networking and exposure decisions

- **Cilium owns networking.** `config.yaml` disables Flannel, kube-proxy,
  Traefik, and ServiceLB; `cilium-values.yaml` runs the full eBPF datapath
  (`kubeProxyReplacement: true`, `bpf.masquerade: true`).
- **The API must be reachable at the node IP.** k3s advertises the
  `kubernetes` Service endpoint as the node IP, so Pods reach the API through
  Cilium's kube-proxy replacement at that address. **Do not set a loopback
  `bind-address`** — it makes every Pod unable to reach the API (and DNS) while
  the node still reports Ready. Port 6443 therefore listens on the host's
  interfaces; restrict it with firewalld if LAN exposure is a concern.
- **LoadBalancer exposure via Cilium LB IPAM (no MetalLB).** Cilium assigns
  LoadBalancer IPs from `cilium-lb-ipam.yaml` (`192.168.0.240-192.168.0.250` —
  adjust to a free range outside your DHCP pool) and announces them on the LAN
  via L2. This is the native replacement for the k3d profile's MetalLB.
- **Cilium shared Ingress is enabled** (LoadBalancer + NodePort 30080), routing
  host `localhost` to Hubble UI and `argocd.localhost` to Argo CD.
- **Argo CD is headless/no-auth**, exposed exactly like the k3d profile: a
  LoadBalancer Service plus the `argocd.localhost` ingress.
  ⚠️ `AGENTS.md` currently says to keep Argo CD on an internal ClusterIP only.
  This profile deliberately follows the stack instead, so **that rule is out of
  date for native k3s** and should be reconciled before relying on it.
- **URLs:** Hubble UI `http://localhost:30080/`, Argo CD
  `http://argocd.localhost:30080/` (or the Cilium LoadBalancer IP with the
  matching Host header).

## Pod swap

`config.yaml` sets `kubelet-arg: [fail-swap-on=false]` so kubelet starts with
the host's zram swap present. Enabling swap **for Pods** is a further kubelet
choice — `memorySwap.swapBehavior: LimitedSwap` (cgroup v2) — with the caveat
that swap-backed Pods trade speed for headroom. It is treated as a safety
cushion, not a capacity plan.

## Usage

```bash
# Full bootstrap (requires sudo; installs a system service)
sudo bash config/k3s/bootstrap.sh

# Or stage by stage
sudo bash config/k3s/install-k3s.sh
bash config/k3s/install-cilium.sh
bash config/argocd/install.sh
bash config/k3s/validate.sh

# Inspect
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl get nodes -o wide
cilium status --namespace kube-system
hubble status --port-forward
hubble observe --port-forward --namespace kube-system --follow

# Tear down (requires sudo; leaves k3d untouched)
sudo bash config/k3s/delete.sh
```

## Notes

- `config/argocd/install.sh` and `config/argocd/values.yaml` are reused
  unchanged: they already describe the headless, `ClusterIP`, ingress-disabled
  contract this profile requires.
- The k3d-specific Cilium values in `config/helm/cilium/values.yaml` are **not**
  used here; `cilium-values.yaml` in this directory is self-contained and
  documents every deliberate difference.
- No credentials, kubeconfigs, certificates, or tokens belong in this
  directory. Keep local overrides outside Git.
