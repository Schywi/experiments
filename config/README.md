# Docker-backed k3d cluster configuration

This directory contains the public, non-secret inputs used to bootstrap and
validate a local k3d cluster. k3d runs k3s inside Docker; this repository does
not install k3s on bare metal or a host VM. It is intentionally separate from the
repository root so application code can be added later without changing the
platform entry points.

```text
config/
├── metallb/                 # local Docker-bridge LoadBalancer installation and pool
├── sealed-secrets/          # Sealed Secrets controller for commit-safe secrets
├── cartography/             # Cartography infrastructure graph (Neo4j + Kubernetes sync)
├── helm/
│   └── cilium/values.yaml   # shared Cilium chart defaults
└── k3d/
    ├── bootstrap.sh          # manual sequential platform bootstrap
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

The scripts expect Docker, k3d, Helm, and kubectl. For the normal workflow,
run the repository entrypoint:

```text
bash start.sh
```

`start.sh` deletes and recreates the named k3d cluster, then runs every platform
and Worm deployment stage directly. It does not require Tilt. The standalone
`delete.sh` is available for deliberate manual deletion.

`config/k3d/bootstrap.sh` remains available for a manual, non-Tilt sequential
bootstrap and uses the same stage scripts.

The bootstrap configures and verifies Docker Hub DNS in every k3d node, then
imports the pinned local runtime images before installation. It uses Cloudflare
IPv4 resolvers by default; set `K3D_DNS_SERVERS` to a comma-separated list of
approved IPv4 resolvers when the local network requires different DNS servers.
The
k3d create script disables Flannel, kube-proxy, Traefik, and ServiceLB so
that Cilium owns networking. It creates exactly one server and no agents; the
server container is hard-capped at 2 GiB. k3d's small API load balancer binds
the Kubernetes API to localhost port 6445. Cilium's shared Ingress Controller
uses the k3d-mapped NodePort 30080, bound to `127.0.0.1:8080`: host
`localhost` routes to Hubble UI and host `argocd.localhost` routes to the
internal Argo CD server Service. `CILIUM_VERSION` is pinned in the script and
should only be changed in a reviewed update.

No credentials, kubeconfigs, certificates, tokens, or other secret-bearing
material belongs in this directory. `config/sealed-secrets/` installs the Sealed
Secrets controller, and secrets are carried as committed `SealedSecret`
resources that only the in-cluster controller can decrypt; keep the controller's
private key backed up out of band. `config/cartography/` deploys the Cartography
infrastructure graph (a bundled Neo4j plus a scheduled Kubernetes sync); its
Neo4j runs without authentication.
