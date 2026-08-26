# Phase 2 — Node agent, heartbeat, and GPU jobs

## Objective

Phase 2 turns Houdry from one-shot inventory into a living fabric:

```text
houdry serve
houdry node join
# node appears READY with heartbeat
houdry job submit gpu.smoke --wait
# agent executes on the node and returns a result
```

Local and remote nodes use the **same HTTP APIs**. There is no special local mode.

## Architecture

```text
Control plane (houdry serve)
        │
        │  POST /v1/nodes/join
        │  POST /v1/nodes/heartbeat
        │  POST /v1/jobs/claim
        │  POST /v1/jobs/{id}/result
        ▼
Node agent (houdry node join)
        │
        ▼
   GPU Runtime adapters
   (nvidia | amd-rocm | inventory | future)
        │
        ▼
       GPU
```

Workloads such as `gpu.smoke` talk only to the **GPU Runtime** interface.
Vendor tools like `nvidia-smi` live inside a runtime adapter, not inside the
job type. That keeps Houdry vendor/runtime agnostic for Phase 3+.

Multiple agents can join one control plane. For development they may share one
physical GPU; do not advertise that as real multi-GPU capacity.

## Commands

```bash
# Terminal 1 — control plane
houdry serve --listen 0.0.0.0:18080

# Terminal 2 — node agent (blocks, heartbeats, claims jobs)
houdry node join --server http://127.0.0.1:18080

# Terminal 3 — submit a GPU smoke job and wait
houdry job submit gpu.smoke --server http://127.0.0.1:18080 --wait
```

Related:

```bash
houdry gpu detect                 # local inventory only
houdry gpu join                   # one-shot inventory snapshot (status JOINED)
houdry gpu list                   # list nodes
houdry job list
houdry job get JOB_ID
```

## Node status

| Status | Meaning |
|---|---|
| `JOINED` | Inventory snapshot only (`gpu join`), no agent heartbeat |
| `READY` | Agent heartbeating and idle |
| `BUSY` | Agent running a claimed job |
| `OFFLINE` | Agent stopped heartbeating (default timeout ~20s) |

## Jobs

Supported type in Phase 2:

- `gpu.smoke` — portable test workload:

  ```text
  gpu.smoke → Node Agent → GPU Runtime → GPU → Result
  ```

  Built-in runtimes today: `nvidia`, `amd-rocm`, and fallback `inventory`
  (normalized discovery). The job fails only if no runtime reports a live GPU.
  Do not treat `nvidia-smi` as part of the job contract.

  Smoke results separate physical GPUs from runtime probes:

  ```json
  {
    "physical_device_count": 1,
    "runtime_probe_count": 2,
    "ok_runtime_count": 2,
    "ok": true
  }
  ```

  Never use probe count as GPU capacity for scheduling.

Job flow:

1. Client `POST /v1/jobs`
2. Agent `POST /v1/jobs/claim`
3. Agent executes locally
4. Agent `POST /v1/jobs/{id}/result`
5. Control plane marks node `READY`

## HTTP API additions

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/nodes/heartbeat` | Agent presence + inventory refresh |
| `POST` | `/v1/jobs` | Submit job (`type`, optional `node_id`) |
| `GET` | `/v1/jobs` | List jobs |
| `GET` | `/v1/jobs/{id}` | Get job |
| `POST` | `/v1/jobs/claim` | Agent claims next pending job |
| `POST` | `/v1/jobs/{id}/result` | Agent reports success/failure |

Join accepts optional `agent_version` and `status`. Inventory-only clients remain
compatible and stay `JOINED`.

## Out of scope for Phase 2

- Multi-GPU packing / fair scheduling
- Model serving / LLM inference
- Agentic workspace
- Claiming that several simulated agents are independent GPUs
- Production TLS / auth beyond the shared token

## Success criteria

On one laptop:

1. `houdry serve` starts
2. `houdry node join` registers and stays `READY`
3. Dashboard shows the node
4. Heartbeat keeps `last_seen` fresh; stopping the agent eventually marks `OFFLINE`
5. `houdry job submit gpu.smoke --wait` succeeds and returns GPU inventory
6. The path is identical if the agent later runs on a second machine

After that, a second physical laptop is an integration proof, not an architecture change.
