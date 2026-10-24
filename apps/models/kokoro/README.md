# Kokoro TTS (CPU)

Kokoro-82M text-to-speech on onnxruntime, served behind a small OpenAI-style
API (`POST /v1/audio/speech` → WAV). CPU-only: the GPU wrappers are CUDA-only,
which this host's AMD RX 570 cannot use.

```text
apps/models/kokoro/
├── Containerfile        # python:3.12-slim + kokoro-onnx + fastapi/uvicorn
├── server.py            # OpenAI-compatible wrapper (WAV output)
└── chart/               # Deployment + ClusterIP Service + weights PVC
```

## Build and push (on the host, rootless)

```bash
config/registry/install.sh                # once: runs the in-cluster registry
buildah bud -t kokoro-tts:local apps/models/kokoro
config/registry/push-image.sh kokoro-tts  # -> 127.0.0.1:5000/kokoro-tts:local
```

## Deploy

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl create namespace models
helm upgrade --install kokoro apps/models/kokoro/chart -n models
```

The init container downloads `kokoro-v1.0.onnx` + `voices-v1.0.bin` onto the
`kokoro-models` PVC (skips files already present).

## Call it

```bash
kubectl -n models port-forward svc/kokoro 8000:8000
curl -s localhost:8000/v1/audio/speech -H 'content-type: application/json' \
  -d '{"input":"Hello from Kokoro on Kubernetes.","voice":"af_sarah"}' \
  --output /tmp/kokoro.wav
```

## Notes

- Footprint: ~310 MB fp32 model, ~0.6 GiB resident. Faster than realtime on this
  CPU (RTF ≈ 1.4× on an older Xeon; this i5 is quicker).
- WAV only (no ffmpeg in the image) to keep it small.
- The upstream release URL is pinned in `values.yaml`; change it there if the
  kokoro-onnx model-files release moves.
