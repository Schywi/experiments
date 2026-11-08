# minio

The object store the session persistence arm writes to. `apps/session-lab`'s
`cmd/persist-lab` opens an S3 client against it, writes one gzipped blob per
persisted session, and deletes the oldest blobs to stay inside
`--persist-budget`.

This chart is separate from `config/session-lab` on purpose. That release owns
the pool autoscaler, and its replica bound is passed on the command line
(`--set autoscaler.maxReplicas=40`) rather than kept in `values.yaml`, so a
`helm upgrade` of that release to add anything would rewrite the autoscaler's
spec. A separate release cannot touch it.

## Install

```bash
helm upgrade --install minio config/minio --namespace sandbox-demo
```

The Service is `LoadBalancer` on **`192.168.0.251`**, from `lan-pool` in
`config/k3s/cilium-lb-ipam.yaml` — the same Cilium LB IPAM and L2 announcement every other
service here uses. That pool's original block (`.240-.250`) is fully allocated, so a second
block `.251-.254` was added; `.255` is the subnet's broadcast address and is deliberately
outside it.

| | |
|---|---|
| S3 API | `http://192.168.0.251:9000` |
| Console, no login | **`http://192.168.0.252/`** |
| Console, direct | `http://192.168.0.251:9001` (asks for the root credentials) |

## The console gateway

MinIO's console has no anonymous mode, so `minio-console-gateway` does what
`config/kargo/gateway` does for Kargo: it logs in once with the root credentials from
Secret `minio-credentials`, keeps the `token` cookie the console answers with, and puts
that cookie on every request it forwards. `http://192.168.0.252/` therefore opens with no
form, and any client can call the console API without credentials:

```bash
curl -s http://192.168.0.252/api/v1/buckets
# {"buckets":[{"name":"session-lab","objects":7,"size":476,...}],"total":1}
```

Two implementation details that cost time and are worth keeping:

- The console's login is `POST /api/v1/login` with a JSON body, and it answers **204 with a
  `token=<jwt>` cookie** — there is no JSON body to parse.
- The cookie has to be forwarded **raw**. Percent-encoding it turns a valid session into
  `403 invalid session`; both forms were checked against the running server.

It is not a general-purpose proxy: hop-by-hop headers and the client's own `Authorization`
and `Cookie` headers are stripped, responses are buffered, so WebSocket-backed console
features are not proxied. Object browsing, upload, download and delete are plain HTTP and
work. The S3 API is not proxied — `cmd/persist-lab` signs its own requests to `minio:9000`.


The bucket `session-lab` is created by the writer on its first PUT; runs are separated by the
key prefix `runs/<run-id>/`. Credentials are generated on first install and read back on
every upgrade through `lookup`, so a `helm upgrade` keeps the same password and nothing
secret is committed here:

```bash
kubectl -n sandbox-demo get secret minio-credentials \
  -o jsonpath='{.data.accessKey}' | base64 -d; echo
kubectl -n sandbox-demo get secret minio-credentials \
  -o jsonpath='{.data.secretKey}' | base64 -d; echo
```

Point the experiment at it — nothing to pass, `cmd/persist-lab` reads the Service and the
Secret from the cluster:

```bash
scripts/persist-experiment.sh --sessions 50 --rate 1 --hold 20s \
  --persist-rate 0.2 --restore-rate 0.2
```

The console runs because the container starts `/opt/bitnami/minio/bin/minio` directly with
`--console-address=:9001`. The image's own entrypoint starts the server without a console
address (leaving 9001 refusing connections) and tries to fetch an `mc` client from
`dl.min.io`, which now answers 410.

## Limits

One replica on one PVC, by design: the persist arm's byte budget is what bounds this store.
Replication would need erasure-set sizing, which is a different experiment. Data is not
backed up — a lost PVC loses the blobs, and the arm's contract is that a session that cannot
be restored costs one page load.
