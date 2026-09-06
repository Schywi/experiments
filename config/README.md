# Docker-backed k3d cluster configuration

This directory contains the public, non-secret inputs used to bootstrap and
validate a local k3d cluster. k3d runs k3s inside Docker; this repository does
not install k3s on bare metal or a host VM. It is intentionally separate from the
repository root so application code can be added later without changing the
platform entry points.

```text
config/
├── helm/
│   └── cilium/values.yaml   # shared Cilium chart defaults
└── k3d/
    ├── bootstrap.sh          # one-click full platform bootstrap
    ├── create.sh             # one Docker-backed k3s server, capped at 2 GiB
    ├── configure-node-dns.sh # configure and verify node DNS before pulls
    ├── delete.sh             # remove the local Docker-backed cluster
    ├── import-images.sh      # import the pinned local runtime images
    ├── install-cilium.sh     # Cilium Helm installation and Hubble Ingress
    └── validate.sh            # node, Cilium, and Hubble readiness checks
```

`config/k3d/wasmtime/` contains a separately invoked, checksum-pinned
preparation package for adding the runwasi Wasmtime shim to the existing k3d
server. It deliberately does not run as part of bootstrap because it mutates
and restarts the server node; see its README before use.

The scripts expect Docker, k3d, Helm, and kubectl. Create the local cluster,
then install Cilium:

```bash
config/k3d/bootstrap.sh
```

The root `Tiltfile` owns the local platform lifecycle. `tilt up` first removes
any existing `cilium-lab` cluster, then creates and bootstraps a fresh one.
`tilt down` stops the lifecycle supervisor, which deletes `cilium-lab` and its
Docker resources.

The bootstrap configures and verifies Docker Hub DNS in every k3d node, then
imports the pinned local runtime images before installation. It uses Cloudflare
IPv4 resolvers by default; set `K3D_DNS_SERVERS` to a comma-separated list of
approved IPv4 resolvers when the local network requires different DNS servers.
The
k3d create script disables Flannel, kube-proxy, Traefik, and ServiceLB so
that Cilium owns networking. It creates exactly one server and no agents; the
server container is hard-capped at 2 GiB. k3d's small API load balancer binds
the Kubernetes API to localhost port 6445. Cilium's Ingress Controller routes
`http://localhost:8080/` to the internal Hubble UI ClusterIP Service through
the k3d-mapped NodePort 30080. `CILIUM_VERSION` is pinned in the script and
should only be changed in a reviewed update.

No credentials, kubeconfigs, certificates, tokens, or other secret-bearing
material belongs in this directory. Keep local overrides outside Git and use
an approved encryption mechanism such as Sealed Secrets if a future
declarative secret is required.
