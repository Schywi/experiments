# Bounded Worm replication experiment

This repository is a reproducible, local Kubernetes research lab for bounded,
observable self-replicating workers. A Go controller turns idempotent
replication intents into capped Deployment scaling; Rust workers emit
telemetry to Vector; and an Elixir regression service analyzes the resulting
stream. Cilium and Hubble expose the network behavior while k3d, Helm, and
local image delivery keep the experiment repeatable.

The experiment is intentionally local. It is not a production Kubernetes
distribution, an Internet-facing service, or a generic application template.

## What runs

```text
                             127.0.0.1:8080
                                    |
                            Cilium shared Ingress
                               |             |
                        localhost        argocd.localhost
                               |             |
                          Hubble UI      Argo CD UI

Worm worker Pods --> stdout --> Vector --> Regression service
       |                                      ^
       `-- replication intent --> Worm controller
                                      |
                                      `-- scales worker Deployment
```

| Component | Role |
| --- | --- |
| k3d/k3s | One Docker-backed k3s server, no agents, hard-capped at 2 GiB. |
| Cilium | CNI, kube-proxy replacement, Cilium Ingress, Envoy, Hubble Relay, and Hubble UI. |
| MetalLB | Layer-2 LoadBalancer addresses on the local Docker bridge. |
| Argo CD | Headless/no-auth local GitOps control plane. |
| Worm controller | Go controller that reconciles the `Worm` CRD and owns worker scale changes. |
| Worm worker | Native Rust Pod that emits samples and one retry-safe replication intent. |
| Vector | Reads the workers' node log files and sends samples to Regression. |
| Regression | Elixir HTTP service that maintains a bounded rolling linear regression. |

The workload is deliberately bounded. The controller limits scaling with the
`Worm` resource's `spec.maxReplicas`; the worker chart also has a namespace
memory quota. A desired worker count above the available quota is expected to
remain pending rather than bypassing the cap.

## Quick start

Prerequisites on the Docker host:

- Docker, with the daemon running
- k3d
- Helm
- kubectl
- curl
- flock

Run the complete local pipeline:

```bash
bash start.sh
```

`start.sh` is intentionally destructive for the named local cluster. It takes
an exclusive lock, deletes `cilium-lab` if it exists, then creates a clean
cluster and runs these stages in order:

1. Create k3d and its local registry.
2. Configure DNS in the k3d node.
3. Import required platform images.
4. Install Cilium and Hubble.
5. Install Argo CD.
6. Build/import MetalLB images and install/configure MetalLB.
7. Enable Cilium's MetalLB-backed ingress and apply Argo CD ingress.
8. Validate the platform.
9. Build/import/deploy Controller, Regression, Vector, and Worker.

The application image builds use `docker build --pull --no-cache`; a host
Docker cache is not relied upon for Worm images. Each built image is imported
into the k3d runtime and deployed with `imagePullPolicy: Never`.

To use a different cluster name, set it for the command:

```bash
K3D_CLUSTER_NAME=my-lab bash start.sh
```

The default cluster is `cilium-lab`; its kubectl context is
`k3d-cilium-lab`.

## Local URLs

The k3d load balancer exposes the shared Cilium ingress only on
`127.0.0.1:8080`.

| Service | URL | Exposure |
| --- | --- | --- |
| Hubble UI | <http://localhost:8080/> | Local Docker host only. |
| Argo CD | <http://argocd.localhost:8080/> | Local Docker host only. |
| Kubernetes API | `https://127.0.0.1:6445` | Local Docker host only. |

The host names select routes on the same listener. They are not different
public IP addresses. `.localhost` resolves to the local host in normal browser
and OS resolver implementations.

Argo CD is configured for this private development cluster with no login:
`server.insecure=true`, `server.disable.auth=true`, and the admin account
disabled. Do not reuse that configuration outside this local experiment.

## Observing the experiment

Open Hubble UI, select `worm-lab`, and generate or wait for worker traffic.
Hubble is event-driven; an idle namespace has no graph to draw. The Cilium
configuration keeps trace aggregation at `none` so short-lived local service
traffic is retained for this lab.

Useful checks from a separate terminal:

```bash
kubectl get pods -n worm-lab -o wide
kubectl get worm -n worm-lab
kubectl get deployment -n worm-lab worm-worker
kubectl logs -n worm-lab deployment/regression --tail=50
cilium status --namespace kube-system
hubble version
```

The local `hubble` CLI is expected to match the Cilium release when possible.
Relay can be checked without publishing an additional host port:

```bash
hubble status --port-forward
hubble observe --port-forward --namespace worm-lab --follow
```

The Regression service accepts `POST /ingest` and exposes its rolling result at
`GET /result`. It is internal to the cluster; inspect it through a port-forward
or from a Pod rather than adding a public service.

## Platform design

### k3d

`config/k3d/create.sh` creates exactly one server and zero agents with:

- k3s `v1.30.6-k3s1`
- 2 GiB server memory cap
- Docker subnet `172.20.0.0/16`
- local registry `cilium-lab-registry.localhost:5000`
- Flannel, kube-proxy, Traefik, ServiceLB, and k3s network policy disabled
- NodePort `30080` mapped to `127.0.0.1:8080`

Cilium therefore owns cluster networking and ingress. MetalLB assigns
addresses from `172.20.0.240-172.20.0.250` on that Docker bridge; those
addresses are private to the host/bridge environment, not Internet-routable.

### Cilium and Hubble

Cilium is pinned by the installer to `1.20.1`. The shared public values live in
[`config/helm/cilium/values.yaml`](config/helm/cilium/values.yaml).

- kube-proxy replacement is enabled.
- The k3d host uses supported iptables masquerading because its kernel rejects
  Cilium 1.20's BPF SNAT program.
- Host legacy routing remains enabled for the local Envoy/ingress path.
- Hubble Relay and UI are enabled.
- Hubble UI frontend/backend use reviewed digest-pinned `quay.io/cilium` images.

The Cilium images are the explicit exception to the repository's normal
Docker-Hub-only external image policy. Repository-built application images are
served from the in-cluster registry at `127.0.0.1:5000/worm-controller:tilt`,
`127.0.0.1:5000/worm-regression:tilt`, and `127.0.0.1:5000/worm-worker:tilt`
(built in-cluster by Kaniko; see `config/registry/`), never `docker.io/...`
references.

For an existing cluster that predates Cilium 1.20, use the reviewed consecutive
minor upgrade script instead of skipping releases:

```bash
bash config/k3d/upgrade-cilium.sh
```

It temporarily disables Hubble UI during bridge releases and restores it at the
final version. A fresh `bash start.sh` does not need this migration script.

### MetalLB

MetalLB `v0.14.9` is built locally from its pinned source and imported into the
k3d node. Its speaker is configured with the node-local Kubernetes API endpoint
because this is a kube-proxy-free, single-node cluster. See
[`config/metallb/README.md`](config/metallb/README.md) for bridge and ARP
details.

### Argo CD

Argo CD chart `10.8.0` is installed in namespace `argocd`. Its application
manifests live under [`config/argocd/applications`](config/argocd/applications).
The Vector application can reconcile from the public repository; the stable
Worm handoff applications pin a Git revision and require deliberate manual
ownership decisions.

Do not register the stable Worm applications while Tilt or the local deployment
scripts own the same resources. Two reconcilers managing one Deployment creates
unreliable results.

### Observability (Cilium/Hubble metrics)

Cilium and Hubble export Prometheus metrics (enabled in the Cilium values
files). `config/victoriametrics/` runs a single-node VictoriaMetrics store that
scrapes the Cilium agent, operator, Hubble agent, and Hubble relay endpoints,
and `config/grafana/` serves the official Cilium/Hubble dashboards from a
provisioned datasource pointing at that store. Argo CD owns both through
`config/argocd/applications/{victoriametrics,grafana}.yaml` in the
`observability` namespace. Open `http://grafana.localhost/`. See
[`config/grafana/README.md`](config/grafana/README.md) and
[`config/victoriametrics/README.md`](config/victoriametrics/README.md).

## Worm data and control paths

1. Each Worker starts with its Pod UID as `WORM_ID`.
2. It sends a single idempotent replication intent to the Controller, retrying
   at most five times with bounded backoff.
3. The Controller records distinct intent IDs in the `Worm` status and updates
   the Worker Deployment scale, never exceeding `spec.maxReplicas`.
4. Every Worker writes JSON samples to stdout at a minimum one-second interval.
5. Vector reads only matching worker Pod logs from the single node and posts
   samples to Regression.
6. Regression de-duplicates `{worm_id, sequence}` in a 60-second, 1,200-sample
   in-memory window and serves the rolling linear regression.

This keeps control-plane replication, data-plane delivery, and network
observation separate. Workers do not hold Kubernetes credentials and do not
scale themselves.

## Tilt

Tilt is optional. The normal one-command bootstrap is `bash start.sh`; it does
not require Tilt. If you use Tilt after that reset, the root `Tiltfile` exposes
separate sidebar resources for cluster, DNS, platform images, Cilium, Argo CD,
MetalLB, ingress, validation, and each Worm workload. It does not delete the
cluster itself.

Do not run `tilt up` from an automation agent. Tilt runtime control belongs to
the developer operating the experiment.

## Configuration knobs

| Variable | Default | Purpose |
| --- | --- | --- |
| `K3D_CLUSTER_NAME` | `cilium-lab` | k3d cluster name. |
| `K3D_DNS_SERVERS` | Cloudflare IPv4 resolvers | Comma-separated node DNS resolvers. |
| `K3D_API_PORT` | `6445` | Local Kubernetes API port. |
| `HELM_TIMEOUT` | `10m` | Helm/rollout timeout used by installation scripts. |
| `KUBECTL_TIMEOUT` | `5m` | Kubernetes validation timeout. |
| `WORM_NAMESPACE` | `worm-lab` | Namespace used by the deployment script. |
| `WORM_NAME` | `worm-lab` | `Worm` custom resource reconciled by the controller. |
| `WORM_RECONCILE_TIMEOUT_SECONDS` | `120` | Time allowed for initial Worm reconciliation. |
| `WORM_SAMPLE_INTERVAL_MS` | `1000` | Worker sample interval; values below 1000 are rejected. |

Keep credentials, kubeconfigs, certificates, `.env` files, and tokens outside
the repository. This is a public repository.

## Troubleshooting

| Symptom | First check | Likely cause / action |
| --- | --- | --- |
| `start.sh` removes a cluster | This is by design. | It always resets the named local k3d cluster. Back up anything important first. |
| `ErrImageNeverPull` | `k3d image import` output and node runtime images. | A local `imagePullPolicy: Never` image was not imported. Re-run the owning build/import stage. |
| `k3d runtime does not contain imported image` | `scripts/build-and-import-image.sh`. | The host-only image tag does not exactly match the deployed image. |
| `x509: certificate signed by unknown authority` after Tilt | Whether the cluster was recreated while Tilt was running. | Stop using the old Tilt session; recreate/reset before starting Tilt so its Kubernetes client uses the current cluster CA. |
| MetalLB speaker does not start | `kubectl get pods -n metallb-system`. | Confirm locally built MetalLB images were imported and its node-local API environment is present. |
| Hubble UI has no graph | Hubble namespace selector and live traffic. | Select `worm-lab` and generate traffic; then check `cilium status` and `hubble status --port-forward`. |
| Worker replicas stay pending | `kubectl describe pod -n worm-lab <pod>`. | The `worm-worker-cap` ResourceQuota is enforcing the experiment's memory cap. |
| Argo route fails after a fresh bootstrap | `config/argocd/values.yaml`, `install-ingress.sh`, and `validate.sh`. | See the current known mismatch below. |

### Current known mismatch

The intended Argo CD model is a `ClusterIP` server reached only through the
local Cilium ingress. That is what `config/argocd/values.yaml` declares.
However, `config/argocd/install-ingress.sh` and `config/k3d/validate.sh` still
assert that `argocd-server` is a `LoadBalancer`. This contradiction can stop a
fresh `bash start.sh` after Argo CD installation. Resolve that script/values
mismatch before treating a fresh bootstrap as fully validated.

## Repository map

```text
apps/                    Go controller, Rust worker, Elixir regression charts
config/                  Deployable k3d, Cilium, MetalLB, Argo CD, and Vector config
scripts/                 Image build/import and direct Worm deployment scripts
Tiltfile*                Optional, resource-oriented Tilt entrypoints
start.sh                 Destructive one-command local bootstrap
blog/ and research/      Design notes and local research material
research/ai-workflow.md  How the local AI assistant is built, shipped, and run
research/reasoner-and-topology.md  Reasoning-model + topology-tool investigation
research/evidence-provenance.md     Evidence provenance, deep links, and cartography plan
```

Read [`AGENTS.md`](AGENTS.md) before automating changes. It contains the
repository safety rules, image policy, commit requirements, and the prohibition
on agents controlling Tilt.
