# Cartography

[Cartography](https://github.com/cartography-cncf/cartography) pulls infrastructure
assets and their relationships into a Neo4j graph. This component bundles the
Neo4j database and a scheduled Cartography sync that reads this cluster's
Kubernetes resources.

```text
cartography/
├── Chart.yaml
├── values.yaml
├── templates/
│   ├── _helpers.tpl
│   ├── serviceaccount.yaml
│   ├── rbac.yaml             # cartography-viewer ClusterRole + binding
│   ├── neo4j.yaml            # Neo4j Deployment + Service + PVC
│   ├── neo4j-secret.yaml     # SealedSecret (Neo4j credentials)
│   ├── cronjob.yaml          # scheduled Cartography sync
│   └── ciliumnetworkpolicy.yaml
└── README.md
```

## Prerequisites

The Sealed Secrets controller must already be installed
(`config/sealed-secrets/install.sh`) so `neo4j-secret.yaml` can be decrypted.
This component is **bound to namespace `cartography`**: the `SealedSecret` in
`neo4j-secret.yaml` was sealed with strict scope for that namespace and name, so
it only decrypts when applied there.

## Deploy

Installed by Argo CD through `config/argocd/applications/cartography.yaml`:

```bash
kubectl apply -f config/argocd/applications/cartography.yaml
```

The chart renders Neo4j plus a Cartography `CronJob` (default every 6 hours). The
sync writes to `bolt://cartography-neo4j:7687`.

## Access

Neo4j Browser is exposed through the Cilium Ingress (`neo4j.localhost`,
`cartography.localhost`) and the Neo4j LoadBalancer (`neo4j.loadBalancerIP`,
which also carries bolt on 7687). Because browsers resolve `*.localhost` to
loopback, map the hostnames first:

```bash
scripts/hosts-entries.sh          # print the /etc/hosts lines
scripts/hosts-entries.sh --apply  # append the missing lines (needs sudo)
```

Then open:

- `http://neo4j.localhost` (or `http://cartography.localhost`) — Neo4j Browser
- `neo4j://neo4j.localhost:7687` — the bolt endpoint the Browser connects to

The Neo4j password is in the `cartography-neo4j-auth` Secret, key `password`.

## How the sync authenticates

Cartography needs a kubeconfig **file** and does not read the pod's in-cluster
config directly. The `CronJob` init container builds one at `/kubeconfig/config`
from the auto-rotating ServiceAccount token, so no kubeconfig secret is stored.
The `cartography-viewer` ClusterRole grants read-only cluster access.

## Not covered

The Kubernetes module ingests standard `networking.k8s.io/v1` NetworkPolicies
only. Cilium CRDs (`CiliumNetworkPolicy`, `CiliumEndpoint`, `CiliumIdentity`,
…) and Hubble flows are **not** ingested — Cartography has no Cilium intel
module.

## Rotating the Neo4j password

Re-run the sealing step in `config/sealed-secrets/README.md` for namespace
`cartography` with secret name `cartography-neo4j-auth` (keys `password` and
`auth`, where `auth` is `neo4j/<password>`), then replace
`templates/neo4j-secret.yaml` and let Argo sync.
