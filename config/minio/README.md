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
helm upgrade --install minio config/minio --namespace sandbox-demo \
  --set service.type=NodePort --set service.nodePort=30900
```

`NodePort` because `persist-lab` runs on the host, not in the cluster.
`ClusterIP` is enough when the runner runs in-cluster.

Credentials are generated on first install and read back on every upgrade
through `lookup`, so a `helm upgrade` keeps the same password and nothing secret
is committed here:

```bash
kubectl -n sandbox-demo get secret minio-credentials \
  -o jsonpath='{.data.accessKey}' | base64 -d; echo
kubectl -n sandbox-demo get secret minio-credentials \
  -o jsonpath='{.data.secretKey}' | base64 -d; echo
```

Point the experiment at it:

```bash
PERSIST_S3_ENDPOINT=<node-ip>:30900 \
PERSIST_S3_ACCESS_KEY=... PERSIST_S3_SECRET_KEY=... \
  scripts/persist-experiment.sh --sessions 50 --rate 1 --hold 20s \
    --persist-rate 0.2 --restore-rate 0.2
```

The bucket is created by the writer on its first PUT, so there is no init Job
and no declarative bucket object. The endpoint is whatever your `NodePort` ends
up on — `kubectl -n sandbox-demo get svc minio` prints it.

## Limits

One replica on one PVC, by design: the persist arm's byte budget is what bounds
this store. Replication would need erasure-set sizing, which is a different
experiment. Data is not backed up — a lost PVC loses the blobs, and the arm's
contract is that a session that cannot be restored costs one page load.
