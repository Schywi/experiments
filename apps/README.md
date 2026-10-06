# Application code

This directory is the approved home for the bounded native-worker replication
experiment's application code. It deliberately separates executable software
from the deployable platform configuration in `config/`.

```text
apps/
├── controller/   # Go: Worm CRD, reconciliation, and replication endpoint
├── models/       # CPU model services (Laya decisions, Kokoro TTS, small LLM)
├── regression/   # Elixir: sample ingestion and bounded linear regression
└── worker/       # Native Rust worker Pod
```

The worker is a small native Rust binary. It emits JSON samples to stdout for
Vector's node-log source and sends one idempotent replication intent to the Go
controller. Each worker remains a normal Kubernetes Pod for Cilium/Hubble
visibility.
