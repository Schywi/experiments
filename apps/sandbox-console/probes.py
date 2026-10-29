"""Isolation evidence, measured from inside the sandbox's own Chromium.

Three reachability claims are live, produced by one `Runtime.evaluate` that
issues three `fetch` calls from the page the tile is showing:

* `cluster`   -> `https://10.43.0.1:443` (the `kubernetes` Service ClusterIP)
* `metadata`  -> `https://169.254.169.254` (link-local metadata endpoint)
* `public`    -> `https://1.1.1.1` (the egress rule allows `0.0.0.0/0`)

Classification is by *how* the fetch failed, not just that it failed. The
template's NetworkPolicy drops rather than rejects (experiment 01 recorded
`EAGAIN`, a 4 s hang), so an `AbortController` abort at the deadline means the
packet went nowhere. A fast failure is a completed TCP handshake followed by a
TLS or certificate error, i.e. the target is reachable -- `10.43.0.1:443`
presents a certificate the page cannot validate, which is exactly what a
reachable API server looks like from an unauthenticated browser.

The remaining two claims are spec-derived, because the browser cannot tell them
apart from a fast failure: `sa_token` (`automountServiceAccountToken`) and
`cluster_dns` (`dnsPolicy`, `dnsConfig.nameservers`). The badge labels those
`spec` so the two evidence classes never blur together.
"""

from __future__ import annotations

import json

PROBE_TIMEOUT_PLACEHOLDER = "__TIMEOUT_MS__"

# One round trip for all three probes. `mode: no-cors` keeps a CORS preflight
# from masking the connection outcome -- we only care whether the socket opened.
PROBE_JS = """
(async () => {
  const timeoutMs = __TIMEOUT_MS__;
  async function probe(url) {
    const started = performance.now();
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      await fetch(url, {mode: 'no-cors', cache: 'no-store', signal: controller.signal});
      return {outcome: 'ok', ms: Math.round(performance.now() - started), error: null};
    } catch (e) {
      const elapsed = Math.round(performance.now() - started);
      const aborted = !!(e && (e.name === 'AbortError' || controller.signal.aborted));
      return {outcome: aborted ? 'timeout' : 'refused', ms: elapsed,
              error: String((e && e.name) || e)};
    } finally {
      clearTimeout(timer);
    }
  }
  return JSON.stringify({
    cluster: await probe('https://10.43.0.1:443/'),
    metadata: await probe('https://169.254.169.254/'),
    public: await probe('https://1.1.1.1/'),
  });
})()
"""


def build_expression(timeout_ms: int) -> str:
    return PROBE_JS.replace(PROBE_TIMEOUT_PLACEHOLDER, str(int(timeout_ms)))


def classify(fetch_result: dict, timeout_ms: int) -> str:
    """Turn one fetch observation into `blocked` or `reachable`."""
    outcome = str((fetch_result or {}).get("outcome") or "refused")
    if outcome == "timeout":
        return "blocked"
    if outcome == "ok":
        return "reachable"
    # Fast, non-abort failure: the handshake completed and TLS or the response
    # failed. Nothing was dropped, so the endpoint is reachable.
    return "reachable"


def public_ok(fetch_result: dict, timeout_ms: int) -> bool:
    return classify(fetch_result, timeout_ms) == "reachable"


def parse(raw_value) -> dict:
    """`Runtime.evaluate` returns the JSON string the page produced."""
    if isinstance(raw_value, dict):
        return raw_value
    try:
        parsed = json.loads(str(raw_value))
    except (TypeError, ValueError):
        return {}
    return parsed if isinstance(parsed, dict) else {}


def summarise(observations: dict, timeout_ms: int, spec: dict | None = None) -> dict:
    """Merge live probe results with the spec-derived claims into the badge."""
    spec = spec or {}
    cluster_probe = observations.get("cluster") or {}
    metadata_probe = observations.get("metadata") or {}
    public_probe = observations.get("public") or {}

    reachability = {
        "cluster": classify(cluster_probe, timeout_ms),
        "metadata": classify(metadata_probe, timeout_ms),
        "sa_token": spec.get("sa_token", "unknown"),
        "cluster_dns": spec.get("cluster_dns", "unknown"),
        "egress_public_ok": public_ok(public_probe, timeout_ms),
        "evidence": {
            "cluster": _evidence(cluster_probe),
            "metadata": _evidence(metadata_probe),
            "public": _evidence(public_probe),
            "sa_token": "spec",
            "cluster_dns": "spec",
        },
    }
    return reachability


def _evidence(fetch_result: dict) -> dict:
    return {
        "ms": fetch_result.get("ms"),
        "outcome": fetch_result.get("outcome"),
        "error": fetch_result.get("error"),
        "source": "live",
    }


def spec_claims(pod: dict | None) -> dict:
    """`sa_token` and `cluster_dns` from the pod spec (read-only, no probing)."""
    spec = (pod or {}).get("spec") or {}
    if not spec:
        return {"sa_token": "unknown", "cluster_dns": "unknown"}
    token = spec.get("automountServiceAccountToken")
    # The API omits the field when it was never set; the default is to mount.
    mounted = True if token is None else bool(token)
    dns_policy = str(spec.get("dnsPolicy") or "ClusterFirst")
    nameservers = ((spec.get("dnsConfig") or {}).get("nameservers") or [])
    resolvers = ",".join(str(item) for item in nameservers[:2])
    return {
        "sa_token": "mounted" if mounted else "absent",
        "cluster_dns": "clusterfirst" if dns_policy == "ClusterFirst" else f"{dns_policy.lower()}:{resolvers or 'none'}",
    }
