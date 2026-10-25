# Sandbox specialist (thin client over the console API)

The assistant does not implement sandbox behaviour. The console owns routing,
readiness, the CDP token and frame freshness; the assistant calls its API and
reports what it returns. No second implementation of router routing, readiness or
`?fresh=1` exists here.

## Contract consumed (owned by the console)

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/sandboxes` | inventory with each sandbox's `ready` / `phase` / route |
| `GET` | `/api/sandboxes/{id}/reachability` | reachability evidence |
| `POST` | `/api/sandboxes/{id}/url` `{url}` | navigate |
| `POST` | `/api/sandboxes/{id}/click` `{selector}` | click |
| `POST` | `/api/sandboxes/{id}/scroll` `{dy}` | scroll |
| `GET` | `/sandboxes/{id}/shot` | PNG of the current frame |
| `POST` | `/api/sandboxes` | create (queued through the console) |
| `DELETE` | `/api/sandboxes/{id}` | destroy |

## Tools exposed to the model

| Tool | Level | Notes |
|---|---|---|
| `sandbox_list` | 1 | the console's inventory verbatim; the assistant never recomputes routability |
| `sandbox_open` | 1 | navigate to a URL, then report the frame URL |
| `sandbox_shot` | 1 | current frame as `image_url` (the UI renders it) |

`CONSOLE_URL` (default `http://192.168.0.250`) and `CONSOLE_TIMEOUT` configure it.

## Routing (explicit only)

No autonomous sandbox use. Deterministic phrases:
`open a sandbox` / `create a sandbox` / `launch a sandbox` → propose `create_sandbox`;
`destroy the sandbox` → propose `destroy_sandbox`; `screenshot` → `sandbox_open` +
`sandbox_shot` when a sandbox id is named, otherwise `sandbox_list`.

## Writes

`create_sandbox` and `destroy_sandbox` are level-2 actions. The model proposes; a
human confirms via `POST /actions/confirm`. The assistant holds **no** RBAC on the
sandbox CRDs: every mutation goes through the console, whose Role is the single
reviewed place that decides what may be created.

## Failure

If the console's JSON surface is absent (`404`) or unreachable, the tool raises and
the pipeline records `ok: false` with the error as evidence. No fabricated
inventory, no invented screenshot.
