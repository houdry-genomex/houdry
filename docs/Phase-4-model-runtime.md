# Phase 4 — Model Runtime & Model Management

Houdry can schedule **inference** workloads onto GPU nodes through a pluggable
**Model Runtime** layer. The control plane and scheduler never talk to Ollama
(or vLLM) directly.

```
                    Houdry
                       │
                Model Runtime API
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
       Ollama        vLLM*      llama.cpp*
```

\* Placeholders in this phase (`Available() == false`).

## Status

| Capability | Status |
|------------|--------|
| GPU node | ✅ |
| Runtime discovery | ✅ |
| Model discovery | ✅ |
| Model availability | ✅ |
| Model-aware job | ✅ |
| Scheduler → model node | ✅ |
| Inference execution | ✅ |
| Result reporting | ✅ |
| Runtime abstraction | ✅ |
| Model registry (per-node inventory) | ✅ |

**Proven so far on one laptop:** 1 node · 1 GPU · 1 runtime (Ollama) · 1 model.
That is enough to close Phase 4; multi-node/multi-runtime is not required to start Phase 5.

## Model identity (runtime-agnostic)

Jobs and the scheduler use:

```text
Model
├── name       e.g. qwen2, tinyllama
├── tag        e.g. 0.5b, latest
├── runtime    e.g. ollama | vllm | llama.cpp  (optional on the job)
└── requirements (GPU / VRAM / require_present)
```

Same logical job, different backends — no scheduler change:

```text
model=qwen  tag=7b  runtime=ollama
model=qwen  tag=7b  runtime=vllm
```

CLI shorthand `--model tinyllama` or `--model qwen:7b` is normalized into
`model_name` / `model_tag`. `--runtime` sets the preferred backend only.

### Node inventory

```text
Node
 ├── GPU(s)
 ├── GPU runtimes      (nvidia, inventory, …)
 ├── Model runtimes    (ollama, …)
 └── Models[]
      ├── name / tag / runtime / state / size
```

Lifecycle: `NOT_PRESENT → DOWNLOADING → AVAILABLE → LOADED → UNLOADED`

## Requirements JSON

```json
{
  "type": "inference",
  "requirements": {
    "gpu_required": true,
    "model_name": "tinyllama",
    "model_tag": "latest",
    "model_runtime": "ollama",
    "require_model_present": false
  },
  "payload": { "prompt": "Say hello from Houdry" }
}
```

Scheduler scoring:

1. Fit GPU / VRAM / runtime presence
2. Prefer **LOADED** → **AVAILABLE** → pull-capable runtime
3. If `model_runtime` is set, only that backend’s nodes qualify

## CLI

```bash
# Prerequisites: a Model Runtime on the worker (Ollama today)
ollama pull tinyllama

houdry serve --listen 0.0.0.0:18080
houdry node join --server http://127.0.0.1:18080
houdry model list --server http://127.0.0.1:18080

houdry job submit inference \
  --server http://127.0.0.1:18080 \
  --model tinyllama \
  --prompt "Say hello from Houdry in one short sentence." \
  --wait

# Prefer a specific backend when several exist:
#   --runtime ollama | --runtime vllm
```

Env: `OLLAMA_HOST` (default `http://127.0.0.1:11434`) — **Ollama adapter only**.

## Agent flow

```text
inference job (name + tag + optional runtime)
    ↓
Find Model Runtime (interface)
    ↓
EnsureModel (no-op if present)
    ↓
Infer(prompt)
    ↓
result.text (+ model_name / model_tag / runtime)
```

## Out of scope

OpenHands, agent loops, RAG, OCR, vision pipelines, automatic model selection,
cost optimization, assistant UI.
