# Questions that must produce a given result

Run against the GPU assistant (`assistant-e4-gpu`). Inspect `metrics.planner`,
each fact's `tool`/`ok`/`links`, and the rendered bubble.

```bash
kubectl -n assistant port-forward svc/assistant-e4-gpu 8080:8080
ask() { curl -s -XPOST http://localhost:8080/investigate -H 'content-type: application/json' \
  -d "{\"message\":\"$1\"}" | python3 -c 'import json,sys;d=json.load(sys.stdin);m=d.get("metrics") or {};
print("planner:",m.get("planner"));
[print(" ",f["tool"],"| ok:",f.get("ok"),"| links:",sorted(f.get("links") or {}),("| err: "+str(f["error"])[:60]) if f.get("error") else "") for f in d.get("facts") or []]'; }
```

## Verified

| Question | Expected | Verified |
|---|---|---|
| `are there any dropped packets?` | `route:prometheus`; `query_prometheus` ok; **links: grafana only** | yes |
| `what is the current network traffic?` | `route:prometheus`; a failed query gives **no** grafana link and a visible error | yes |
| `what is happening in the worm-lab namespace?` | `llm`; kube fact with `args.namespace` → **links: hubble only** | yes |
| `what is a pod?` | general intent → **no links** (corpus mounted; planner may pick recall) | yes |
| `take a screenshot` | `route:sandbox`; `sandbox_list` from the console | yes |
| chat tab: `say hi` | reply with `tool: null`, no facts, no links | yes |

## Link rules (assert these)

- At most **one** link kind per answer, chosen by intent.
- `grafana` only when the intent is `prometheus` **and** the query executed.
- `hubble` only when the intent is `kube` **and** the fact carries `args.namespace`.
- `neo4j` only for `get_topology_snapshot` under `cartography`.
- `argocd` and `kargo` never appear (see `links.md`).

## Sandbox (console API)

| Question | Expected |
|---|---|
| `take a screenshot of sandbox <id>` | `sandbox_shot` → `image_url`, rendered as an image in the Evidence row |
| `open a sandbox` | `propose_action` → `create_sandbox` returns a token; executes only after `POST /actions/confirm` |
| `destroy the sandbox <id>` | `propose_action` → `destroy_sandbox`; executes only after confirm |
| any of the above with the console `/api` absent | `ok:false` with `console API unavailable (404)` — no fabricated result |
