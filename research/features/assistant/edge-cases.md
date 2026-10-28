# Edge cases: sandbox URLs

The assistant does not open URLs of its own choosing. The sandbox tools exist so a human can point a
browser at a target that is already on the LAN, and the console is the thing that decides what a URL
may be. Everything below is correct behaviour under that rule.

## Bare hostnames are accepted, schemes are not arbitrary

`apps/sandbox-console/server.py` normalises the input, then checks the scheme:

```python
has_scheme = re.match(r"^[a-zA-Z][a-zA-Z0-9+.\-]*:", text) is not None
candidate  = text if has_scheme else f"https://{text}"     # google.com -> https://google.com
scheme     = (urlparse(candidate).scheme or "").lower()
if scheme not in ALLOWED_URL_SCHEMES:                       # ("http", "https")
    raise HTTPException(400, detail=f"only http, https urls can be opened, not {scheme}")
```

Consequences:

| input | outcome |
|---|---|
| `google.com` | opens as `https://google.com/` |
| `https://example.com/x` | opens unchanged |
| `http://192.168.0.245/` | opens unchanged |
| `file:///etc/passwd` | rejected, `400` — a sandbox cannot paint its own disk into the pane |
| `javascript:alert(1)` | rejected, `400` |

The assistant passes the URL through unchanged. It does not rewrite the scheme, so a rejection is the
console's verdict and stays visible as a failed fact.

## Public targets work; the cluster does not leak

Measured on a live sandbox:

```
open google.com -> 200  {"ok":true,"url":"https://www.google.com/"}
frame          -> 200  image/png  34,279 bytes

reachability: cluster "blocked" (timeout), metadata "blocked" (timeout),
              sa_token "absent", egress_public_ok true (public probe 692 ms)
```

A sandbox can reach the public internet while the Kubernetes API and the metadata endpoint stay
unreachable from inside it. Opening an external page is therefore a supported target, and not an
isolation hole.

## A request that names no URL

`take a screenshot` with no target and no id resolves to `sandbox_list`, so the answer reports what
exists instead of capturing something arbitrary. A question that names an id the console no longer
lists fails with `no sandbox '<id>'; the console reports: <live ids>`.

## Why this is the boundary

The agent was forbidden from opening URLs on its own initiative. A URL reaches the console only when
the question contains one, or when it comes from `provenance` (`http://192.168.0.245/…` for Grafana,
`http://192.168.0.241/?namespace=…` for Cilium). The assistant renders what the LAN accepts, and the
console's scheme check is the last line of it.
