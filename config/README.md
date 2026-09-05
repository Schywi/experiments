# Cluster configuration

This directory contains the public, non-secret inputs used to bootstrap and
validate the local k3s cluster. It is intentionally separate from the
repository root so application code can be added later without changing the
platform entry points.

```text
config/
├── helm/
│   └── cilium/values.yaml   # shared Cilium chart defaults
└── k3s/
    ├── install.sh            # k3s server with the bundled CNI disabled
    ├── install-cilium.sh     # Cilium Helm installation
    └── validate.sh            # node, Cilium, and Hubble readiness checks
```

The scripts expect `curl`, `helm`, and `kubectl` to be installed. Run the k3s
installer as root on a disposable host, then point `kubectl` at the resulting
cluster before installing Cilium:

```bash
sudo config/k3s/install.sh
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
config/k3s/install-cilium.sh
config/k3s/validate.sh
```

The k3s installer disables Flannel, kube-proxy, Traefik, and ServiceLB so that
Cilium owns networking and later platform charts can provide their own
exposure. `K8S_SERVICE_HOST` defaults to `127.0.0.1`, which is suitable for a
single-node local cluster; set it to a reachable node address for a multi-node
cluster. `CILIUM_VERSION` is pinned in the script and should only be changed in
a reviewed update.

No credentials, kubeconfigs, certificates, tokens, or other secret-bearing
material belongs in this directory. Keep local overrides outside Git and use
an approved encryption mechanism such as Sealed Secrets if a future
declarative secret is required.
