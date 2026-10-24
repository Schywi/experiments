# Security edge cases

Security-relevant exposures and gaps found in the assistant. Threat model here is
**not** "public internet" — the assistant is a LAN service — but a browser on the
LAN that visits *any* web page.

---

## S1 — Permissive CORS on an unauthenticated API — **open**

**Added in `e415607`** (`experiments-3nt`): `CORSMiddleware` with
`allow_origins=["*"]` (env `CORS_ORIGINS`, chart `cors.origins`) so a separate UI
(fuwa at `http://192.168.0.244`) can `fetch()` the API.

| | |
|---|---|
| **Surface** | `apps/assistant/server.py` (the middleware); default `cors.origins: "*"` |
| **Verified** | `OPTIONS /investigate` and `POST /chat` from `Origin: http://192.168.0.244` return `access-control-allow-origin: *` |
| **Status** | open (default is `*`) |

### What it is / isn't

CORS is **not** a security boundary and **not** authentication. The assistant has
**no auth at all** — any client on the LAN (or anything that can reach
`192.168.0.243`, the ingress, or `*.home.arpa`) can already call every endpoint.
`allow_origins: "*"` removes the one barrier that *did* exist for the browser: the
same-origin policy. So it widens access from "a page we host" to "**any web page
the user's browser visits**".

### Threat model

A user on the LAN opens `https://some-random-site` (or any page with injected
JS). That page runs `fetch("http://192.168.0.243/investigate", …)` from the
browser, which **can reach LAN IPs**. With `Access-Control-Allow-Origin: *` the
page may **read the response**. (DNS-rebinding is not even required — the LB IP is
directly reachable; the API also does not validate the `Host` header.)

### Impact (depends on config)

| Capability | With defaults | If `rbac.allowActions: true` |
|---|---|---|
| **Read** cluster state — `POST /investigate`, `/chat`, `GET /metrics`, `/plan` | **Yes** — pod names, events, logs, PromQL results, LLM/perf metrics | same |
| **Denial of service** — `POST /investigate`/`/speak`/`/plan` each burn CPU LLM / Kokoro time | **Yes** — a page can queue work and starve the single-replica models | same |
| **Write** — `POST /actions/request` → `POST /actions/confirm` (restart/scale) | No (`actions.enabled:false`) | **Yes** — an attacker mints a token via `/actions/request` and confirms it via `/actions/confirm`, no human in the loop |

The read + DoS cases are live **now**. The write case is one Helm value away
(`rbac.allowActions: true`) — which is the strongest reason to keep actions
disabled and to narrow CORS.

### Why "read-only" doesn't fully save us

Read-only still leaks the cluster's shape (workloads, namespaces, event messages,
log lines from `search_logs`/`get_pod_logs`) and lets a stranger **exhaust the CPU
models** — a real availability issue on a single-node lab.

### Mitigations (least → strongest)

1. **Restrict origins** — set the knob we added:
   ```yaml
   cors:
     origins: "http://192.168.0.244"     # fuwa only; comma-separate for more
   ```
   Note: this is a browser control only — a non-browser client ignores CORS
   entirely, so it stops the *drive-by web page*, not a LAN attacker.
2. **Keep `rbac.allowActions: false`** (it is). Do not expose write verbs.
3. **Network policy** — a Cilium policy limiting who may reach the assistant on
   `8080` (today the `assistant` namespace has no CNP → default allow).
4. **Don't expose it publicly without auth** — if it goes beyond the LAN, put an
   authenticated proxy / a per-request token in front; the service has no auth and
   no rate limit.
5. **If credentials are ever needed**, `allow_origins: "*"` **cannot** be used —
   switch to an explicit origin list.

### Verify

```bash
# CORS is open?
curl -s -D - -o /dev/null 'http://192.168.0.243/healthz' -H 'Origin: http://evil.example' \
  | grep -i access-control-allow-origin          # "*"  => open

# actions (write path) are off?
curl -s 'http://192.168.0.243/actions' | grep '"enabled"'   # false
```

---

## S2 — Prompt injection (followed, low blast radius) — **open (informational)**

The `injection` edge case ("ignore all previous instructions…") was **obeyed** by
the model and, pre-`ve1`, produced a leaked JSON blob. Now the contract is
enforced, but the model will still follow injected instructions in the *question*
and in **tool output** (log lines, event messages) — a hostile log line could try
to steer the answer. Blast radius is low because the tools are read-only and the
model cannot mint links or run actions, but treat all tool content as untrusted
data. Relevant if `allowActions` is ever enabled (then keep the human-confirm
step, which it already has).

## S3 — No authentication, no rate limit — **open (by design, for now)**

Every endpoint is unauthenticated, including `/actions/*`, `/metrics`, and
`/plan` (which triggers LLM calls). Acceptable for a private LAN lab; any wider
exposure needs auth in front (see S1-4).
