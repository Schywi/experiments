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
    ├── delete.sh             # remove the local Docker-backed cluster
    ├── import-images.sh      # import the pinned local runtime images
    ├── install-cilium.sh     # Cilium Helm installation
    └── validate.sh            # node, Cilium, and Hubble readiness checks
```

The scripts expect Docker, k3d, Helm, and kubectl. Create the local cluster,
then install Cilium:

```bash
config/k3d/bootstrap.sh
```

The root `Tiltfile` initializes the k3d, image-import, Cilium, OpenResty, and
no-auth Argo CD resources on `tilt up`. OpenResty and Argo CD depend only on
k3d creation; they do not wait for Cilium validation. The explicit
`platform-bootstrap` resource remains available for a full aggregate run and
applies the Argo CD Application after all branches have been attempted.

The bootstrap imports the pinned local runtime images before installation. The
k3d create script disables Flannel, kube-proxy, Traefik, and ServiceLB so
that Cilium owns networking. It creates exactly one server and no agents; the
server container is hard-capped at 2 GiB. k3d's small API load balancer binds
the Kubernetes API to localhost port 6445. `CILIUM_VERSION` is pinned in the
script and should only be changed in a reviewed update.

No credentials, kubeconfigs, certificates, tokens, or other secret-bearing
material belongs in this directory. Keep local overrides outside Git and use
an approved encryption mechanism such as Sealed Secrets if a future
declarative secret is required.
