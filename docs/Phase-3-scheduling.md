# Phase 3 — Multi-Node Resource Discovery & Basic Scheduling

Houdry can discover, register, monitor, and schedule work across multiple heterogeneous machines on a LAN. Nodes only know the control plane URL — never each other.

## Architecture

```
                 Control Plane
                      │
                  Scheduler
                      │
          ┌───────────┼───────────┐
          ▼           ▼           ▼
       Node A       Node B      Node C
```

## Resource profiles

Each registered agent publishes a normalized profile:

| Layer | Contents |
|-------|----------|
| Identity | `node_id`, hostname, remote IP |
| Static | CPU cores, total RAM, arch, physical GPUs (id/vendor/model/VRAM) |
| Dynamic | used RAM, available VRAM, util, active jobs |
| Runtimes | e.g. `nvidia`, `inventory` (probes — **not** GPUs) |
| Status | `READY` / `BUSY` / `DRAINING` / `OFFLINE` / `JOINED` |

Physical GPUs come from Phase 2 inventory. Runtime probes are listed separately and never counted as GPUs.

## Workload requirements

Jobs carry framework-agnostic requirements:

```json
{
  "type": "gpu.smoke",
  "requirements": {
    "gpu_required": true,
    "min_vram_bytes": 6442450944
  }
}
```

The scheduler does not know whether the job came from CLI, UI, or an agent app.

## Scheduler (first-fit)

1. Job submitted → `queued`
2. Find `READY` nodes that `Fit` (GPU + available VRAM)
3. Assign oldest matching job → `pending` + `node_id`
4. Agent claims → `running`
5. If nothing fits → stay `queued` until a suitable node is `READY`

Offline and draining nodes are excluded.

## Drain / leave / failure

```
READY → DRAINING → (finish job) → leave → removed
READY → heartbeat timeout → OFFLINE (excluded from pool)
```

- `POST /v1/nodes/drain` — no new jobs
- `POST /v1/nodes/leave` — remove when idle
- Ctrl+C on `houdry node join` drains then leaves
- Phase 3 does **not** migrate running jobs

## CLI

```bash
# Control plane (any machine)
houdry serve --listen 0.0.0.0:18080

# Each worker (only needs the server URL)
houdry node join --server http://192.168.1.10:18080

# Cluster view
houdry node list --server http://192.168.1.10:18080

# Schedule by VRAM (picks a node with ≥ 6 GiB available)
houdry job submit gpu.smoke --server http://192.168.1.10:18080 --min-vram-mb 6000 --wait

# Controlled removal
houdry node drain --server http://192.168.1.10:18080
houdry node leave --server http://192.168.1.10:18080
```

## APIs

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/v1/cluster` | Summary + nodes |
| GET | `/v1/nodes` | Node list |
| POST | `/v1/nodes/join` | Register |
| POST | `/v1/nodes/heartbeat` | Live profile |
| POST | `/v1/nodes/drain` | Start drain |
| POST | `/v1/nodes/leave` | Remove when idle |
| POST | `/v1/jobs` | Submit with `requirements` |
| POST | `/v1/jobs/claim` | Agent pull (assigned only) |

## Success demo

1. Machine A (RTX 2050 4 GB) and Machine B (RTX 4060 8 GB) join the same server.
2. Submit `--min-vram-mb 6000` → scheduler selects Machine B.
3. Disconnect Machine B → heartbeat timeout → `OFFLINE`.
4. Submit again → job stays queued / never assigned to B.

## Out of scope

Agent frameworks (OpenHands/OpenClaw), RAG, OCR, vision pipelines, automatic
model selection, cost optimization, assistant UI. Model serving itself moved to
[Phase 4](Phase-4-model-runtime.md).
