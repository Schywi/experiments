# OpenResty Hubble UI proxy

This chart deploys OpenResty as an in-cluster reverse proxy for the Cilium
Hubble UI service. It assumes the Cilium chart is installed in `kube-system`,
where the default service is `hubble-ui`.

The service is `ClusterIP` by default and the chart has no authentication or
credentials. The chart uses the standard Kubernetes `NetworkPolicy` API, so it
can be created before the Cilium CRD exists. It permits DNS and Hubble UI
egress only; `kubectl port-forward` remains the intended local access path.
Add approved frontend namespaces with `networkPolicy.allowedNamespaces`; keep
external exposure behind a separately reviewed ingress or gateway.

Install from the repository root:

```bash
helm upgrade --install cilium-dashboard ./config/openresty \
  --namespace openresty --create-namespace
kubectl -n openresty port-forward svc/cilium-dashboard 8080:80
```

Then open `http://127.0.0.1:8080/`. OpenResty exposes `/healthz` locally and
may return `502` from `/` until the Hubble UI service has ready endpoints. If
the Hubble UI service has a different name or namespace, override
`hubble.service` and `hubble.port` in a local values file; do not commit
machine-specific values or secrets.
