# Worm regression service

Minimal Elixir HTTP service for the worm experiment. It accepts JSON events at
`POST /ingest` and exposes the current rolling linear regression at `GET /result`.

An event uses Unix milliseconds for `occurred_at`:

```json
{"worm_id":"worm-a","sequence":1,"x":1.0,"y":3.0,"occurred_at":1725571200000}
```

Duplicate `{worm_id, sequence}` pairs are ignored while retained in the 60-second
window. The in-memory window is capped at 1,200 samples.

Run locally:

```sh
mix deps.get
mix test
mix run --no-halt
```
