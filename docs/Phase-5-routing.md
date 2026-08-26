# Phase 5 — Intelligent Model & Resource Routing

Phase 5 is **not** “add more models.”

It is the control plane deciding **which model** and **which node** should run a
request, based on task shape and live cluster state.

```text
User request
      ↓
Task analysis          (modality, complexity, capabilities)
      ↓
Model catalog          (logical models + caps + VRAM needs)
      ↓
Cluster state          (READY nodes, loaded/available models, VRAM)
      ↓
Score (model, node)
      ↓
Select → optional inference job
```

## What you've proven

Simple text:

```text
Prompt → complexity=low, caps=chat+simple → tinyllama:latest → RTX 2050
```

Coding-shaped prompt:

```text
Prompt → modality=code, complexity=medium → qwen2.5-coder:1.5b → RTX 2050
```

Houdry is making a **task → model → node** decision, not only executing a
user-specified model.

```text
USER → Task/Prompt → Task Profiler (modality/complexity/capabilities)
                   → ROUTER → Model A|B|C
                   → SCHEDULER → NODE → Model Runtime → LLM
```

## Important limitation (not a routing bug)

A prompt like *"Refactor this Go function and add tests"* may correctly route to
a coding model that replies *"Please provide the function…"*.

That is **expected** in Phase 5: the router selected the right *class* of model.
Houdry does not yet have an **agent/tool layer** (workspace files, edit tools,
multi-step loops) to actually perform the task. That belongs in a later phase.

## Scoring: keep it simple

Current score (capability + complexity fit + availability/LOADED) is enough to
establish the architecture. Do **not** over-invest here yet.

Later (mature routing), optionally add: VRAM/util, queue depth, latency,
quality, token throughput, privacy class, estimated cost.

## Status

| Capability | Status |
|------------|--------|
| Task profile (modality / complexity) | ✅ heuristic analyzer |
| Model catalog (runtime-agnostic) | ✅ default + `model-catalog.json` |
| Prefer loaded / available models | ✅ |
| Prefer smaller model for simple tasks | ✅ |
| Prefer coding model for code tasks | ✅ |
| VRAM / node readiness gates | ✅ |
| `POST /v1/route` (+ optional execute) | ✅ |
| Vision / OCR / PDF pipelines | ⏸ deferred (detected, not executed) |

## Examples

Simple:

```text
"Say hello" → low/text → tinyllama (or other simple-capable) → local GPU
```

Coding:

```text
"Refactor this Go function…" → code/medium → qwen2.5-coder → capable node
```

Later (not Phase 5 execution):

```text
Scanned PDF → document/vision → OCR → vision → reasoning   (deferred)
```

## CLI

```bash
# Catalog the router can choose from
houdry model catalog --server http://127.0.0.1:18080

# Analyze + select (no run)
houdry route --prompt "Say hello from Houdry" --server http://127.0.0.1:18080

# Select + run inference
houdry route --prompt "Say hello from Houdry" --execute --wait --server http://127.0.0.1:18080

# Only models already on a node (no pull)
houdry route --prompt "Fix this Python bug in foo()" --require-model --execute --wait
```

## APIs

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/v1/catalog` | Logical model catalog |
| POST | `/v1/route` | `{prompt, execute?, runtime?, require_model_present?}` |

Catalog file (auto-created): `$HOODRY_HOME/server/model-catalog.json` (or `--data` dir).

## Architecture notes

- Analyzer is **heuristic** (no LLM-as-judge) so routing works before a model is chosen.
- Catalog entries use **name + tag + capabilities**; runtime is optional preference — same as Phase 4 identity.
- Scheduler/job path unchanged: route emits an `inference` job with concrete requirements.
- Best-effort **fallback** uses a present chat model when nothing ideal fits (one-laptop friendly).

## Out of scope

OpenHands, agent loops, RAG, OCR, vision chains, automatic catalog learning,
cost billing, multi-step tool planners.
