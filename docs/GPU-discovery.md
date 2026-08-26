# Phase 1 — GPU discovery and join

## Objective

Phase 1 makes a machine join the Houdry GPU fabric.

The intended flow is the same on Linux, macOS, and Windows:

1. Install Houdry from the server (`curl` on Unix, `irm` on Windows).
2. `houdry gpu detect` — inspect local GPUs.
3. `houdry gpu join` — register those GPUs with the Houdry server.
4. The node appears as joined on the server.

After Phase 1, Houdry can answer:

> What GPU resources does this machine have, and which machines have joined the fabric?

---

## Scope

- Cross-platform CLI (`houdry`) for Linux, macOS, and Windows (amd64 and arm64)
- Local GPU detection
- Vendor, model, memory, driver, compute capability, utilization, temperature
- Multiple GPUs per machine
- A normalized GPU information model
- Install scripts hosted by the Houdry server
- `houdry gpu join` against a local control-plane server
- Server dashboard and API listing joined nodes

---

## Out of scope

- GPU scheduling
- Remote GPU execution
- Model serving and LLM inference
- OpenHands / OpenClaw integration
- Job queues
- Container orchestration
- Distributed computing beyond “this node has joined”

Authentication is optional (`--token` / `HOODRY_TOKEN`) for a private network.

---

## Design

GPU discovery is independent of the rest of Houdry. Detectors produce one normalized model. Nothing else talks to nvidia-smi, sysfs, WMI, or system_profiler.

```text
Operating System / GPU Runtime
              ↓
        GPU Discovery
              ↓
       Normalized GPU Model
              ↓
     houdry gpu detect | join
              ↓
         Houdry Server
```

### Detection sources

| Platform | Sources |
|---|---|
| Linux | nvidia-smi, rocm-smi, sysfs DRM, lspci |
| Windows | nvidia-smi, Win32_VideoController (WMI) |
| macOS | nvidia-smi (Intel Macs), system_profiler (Apple Silicon and displays) |

Results are merged and de-duplicated by GPU UUID or PCI bus ID.

### Install

Unix shells cannot be used as-is on Windows, so the **bootstrap** command differs. The **houdry** commands after install are identical.

| OS | Install |
|---|---|
| Linux, macOS | `curl -fsSL http://SERVER:8080/install.sh \| sh` |
| Windows PowerShell | `irm http://SERVER:8080/install.ps1 \| iex` |

Both scripts download the matching binary, write `~/.houdry/config.json` with the server URL, and put `houdry` on `PATH`.

Then:

```bash
houdry gpu detect
houdry gpu join
```

---

## Commands

```bash
houdry serve --listen 0.0.0.0:8080
houdry gpu detect [--json]
houdry gpu join [--server URL] [--token TOKEN]
houdry gpu list [--server URL]
```
