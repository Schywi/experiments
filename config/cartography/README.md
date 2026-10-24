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
│   ├── cronjob.yaml          # scheduled Cartography sync
│   └── ciliumnetworkpolicy.yaml
└── README.md
```

## Prerequisites

Requires the Cilium Ingress Controller (for the hostnames) and a default
StorageClass for the Neo4j PVC. No secret is needed: Neo4j runs with
authentication disabled (see [Authentication](#authentication)).

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

Authentication is disabled, so connect with any user or no credentials (just
click Connect). Ready-to-run Cypher examples live in
[`queries.cypher`](queries.cypher).

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

## Authentication

Neo4j runs with `NEO4J_AUTH=none` (authentication disabled), so no password is
required. The Cartography sync connects with `auth=None` accordingly (no
`--neo4j-user` or `--neo4j-password-env-var`).

This is a lab convenience: anyone who can reach bolt (7687) or the Browser has
full read/write access to the graph. To re-enable auth later, provide `NEO4J_AUTH`
from a Secret and pass `--neo4j-user` plus `--neo4j-password-env-var` to the sync
(the Sealed Secrets controller in `config/sealed-secrets/` is available for that).
