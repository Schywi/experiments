# MetalLB for the local k3d Docker network

This directory contains only the MetalLB Layer 2 configuration for the
single-node local cluster. It reserves a private address range on the exact
Docker network used by this project:

| Setting | Value |
| --- | --- |
| Docker network | `172.20.0.0/16` |
| Docker host gateway | `172.20.0.1` |
| Kubernetes node | `k3d-cilium-lab-server-0` |
| Node interface | `eth0` |
| MetalLB address pool | `172.20.0.240-172.20.0.250` |

`address-pool.yaml` creates an `IPAddressPool` and an `L2Advertisement`. The
advertisement is restricted to the one k3d server and its `eth0` interface, so
MetalLB announces assigned service IPs on the Docker bridge using ARP. The
pool is private and is intended to be reachable from the Docker host, not from
the wider network.

## Installation dependency and image-policy blocker

MetalLB must be installed before this configuration can be applied. The
official installation supports a native manifest, Kustomize, or Helm, and the
official Layer 2 configuration requires both an `IPAddressPool` and an
`L2Advertisement`:

- Installation: <https://metallb.io/installation/>
- Layer 2 configuration: <https://metallb.io/configuration/>
- Advanced node/interface selection: <https://metallb.io/configuration/_advanced_l2_configuration/>

This repository requires Docker Hub image references for platform workloads.
The current official MetalLB Helm chart defaults its controller and speaker to
non-Docker-Hub image repositories; see the upstream chart values at
<https://github.com/metallb/metallb/blob/main/charts/metallb/values.yaml>.
No verified, project-approved Docker Hub mirror for both components is
declared here. Consequently, this scope intentionally does **not** embed the
upstream installation manifest or an unverified image override. Installing an
unmodified upstream release would violate the repository image policy and may
leave the cluster unable to pull the required images.

Before installation, an operator must select and review a Docker Hub mirror
for both the controller and speaker, pin the images by approved immutable
references, and render/install MetalLB with those overrides. The selected
mirror and exact references must be added in a separately reviewed change;
third-party images are not assumed to be compatible replacements.

## Apply the repository configuration

After the approved MetalLB installation is healthy, run:

```bash
bash config/metallb/configure.sh
```

Then apply `cilium-values.yaml` as a second values file during the Cilium
Helm upgrade. It changes only `kube-system/cilium-ingress` to
`type: LoadBalancer`; Argo CD remains a ClusterIP Service.

The existing `insecureNodePort: 30080` is retained so the k3d loopback mapping
continues to work while MetalLB assigns the Docker-bridge address.

The script is guarded. It checks for the `metallb-system` namespace, both
MetalLB CRDs, and the exact k3d node before applying only the files in this
directory. It does not install MetalLB, change Cilium, change a Service type,
or alter Docker networking.

Equivalent declarative application:

```bash
kubectl apply --server-side --field-manager=experiments-metallb \
  -k config/metallb
```

## Verify an assigned address

MetalLB assigns an address only to a Kubernetes `Service` of type
`LoadBalancer`. After a compatible service exists, inspect the assignment and
the speaker state:

```bash
kubectl get ipaddresspool,l2advertisement -n metallb-system
kubectl get svc -A -o wide
kubectl get pods -n metallb-system -o wide
```

The assigned address should be one of `172.20.0.240` through `172.20.0.250`.
From the Docker host, verify ARP and HTTP routing with the assigned address:

```bash
bridge_iface="$(ip -o route get 172.20.0.240 | awk '{for (i = 1; i <= NF; i++) if ($i == "dev") {print $(i + 1); exit}}')"
arping -I "${bridge_iface}" 172.20.0.240
curl -H 'Host: localhost' http://172.20.0.240/
```

Replace `172.20.0.240` with the address shown in the service status. The
MetalLB configuration allocates and advertises the address; Cilium's ingress
service type and Envoy data path remain separate concerns and are deliberately
outside this directory.
