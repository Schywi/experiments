# Application code

This directory is the approved home for the bounded Lua/Wasm replication
experiment's application code. It deliberately separates executable software
from the deployable platform configuration in `config/`.

```text
apps/
├── controller/   # Go: Worm CRD, reconciliation, and replication endpoint
├── regression/   # Elixir: sample ingestion and bounded linear regression
└── worker/       # Lua compiled/package for the Wasmtime WAsm worker Pod
```

No application implementation is present yet. The current repository state
only prepares the existing k3d node to support `runtimeClassName: wasmtime`.
The controller, CRD, regression application, and worker artifact will be
introduced as independent, reviewable changes under this directory.
