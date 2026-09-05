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
    ├── create.sh             # one Docker-backed k3s server, capped at 2 GiB
    ├── delete.sh             # remove the local Docker-backed cluster
    ├── install-cilium.sh     # Cilium Helm installation
    └── validate.sh            # node, Cilium, and Hubble readiness checks
```

The scripts expect Docker, k3d, Helm, and kubectl. Create the local cluster,
then install Cilium:

```bash
config/k3d/create.sh
config/k3d/install-cilium.sh
config/k3d/validate.sh
```

The k3d create script disables Flannel, kube-proxy, Traefik, and ServiceLB so
that Cilium owns networking. It creates exactly one server, no agents, and no
load balancer; the server container is hard-capped at 2 GiB. The default
context is `k3d-cilium-lab` and the Kubernetes API is bound to localhost port
6445. `CILIUM_VERSION` is pinned in the script and should only be changed in a
reviewed update.

No credentials, kubeconfigs, certificates, tokens, or other secret-bearing
material belongs in this directory. Keep local overrides outside Git and use
an approved encryption mechanism such as Sealed Secrets if a future
declarative secret is required.
