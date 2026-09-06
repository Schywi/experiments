# Native Worm worker

Each worker is one normal Kubernetes Pod running a small static Rust binary.
It has no Kubernetes client, service-account token, filesystem persistence, or
direct Vector dependency. The Go controller alone owns replica scaling.

Required environment variables:

| Variable | Meaning |
| --- | --- |
| `WORM_ID` | Immutable Pod UID. |
| `CONTROLLER_URL` | Internal controller URL, for example `http://worm-controller.worm-lab.svc.cluster.local:8081`. |
| `WORM_SAMPLE_INTERVAL_MS` | Sampling interval; defaults to and must be at least `1000`. |

At startup the worker sends one idempotent intent to the controller. It makes
at most five attempts with a bounded 100–400ms backoff. It then writes one JSON
sample per interval to stdout. Vector reads the container log and forwards the
sample to regression.

Run `cargo test` for the worker unit tests.
