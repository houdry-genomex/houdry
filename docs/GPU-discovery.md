# Phase 1 — GPU discovery and join

## Objective

Phase 1 implements this workflow:

1. Install the Houdry CLI on a node.
2. Run `houdry gpu detect` to inspect GPUs visible to that operating system.
3. Run `houdry gpu join` to send one inventory snapshot to a Houdry server.
4. View the joined node through the server dashboard, API, or `gpu list`.

The post-install CLI commands are the same on Linux, macOS, and Windows. The
bootstrap differs because Windows PowerShell does not execute POSIX shell
scripts.

Phase 1 answers:

> What GPUs can this machine currently report, and what inventory did each
> joined node most recently submit?

It does not prove that a GPU is healthy or schedulable, and it does not keep the
inventory current automatically.

## Implemented scope

- One Go CLI with build targets for Linux, macOS, and Windows on amd64 and arm64
- Multiple-GPU discovery and normalized JSON output
- NVIDIA, AMD/ROCm, Linux DRM/PCI, Windows CIM, and macOS display-profiler
  detection paths
- Best-effort vendor, model, identifiers, memory, driver/runtime, utilization,
  and temperature fields where the selected operating-system tool exposes them
- One-shot node registration over HTTP
- Shared-token protection for the join and node-list APIs
- JSON file persistence, a basic HTML dashboard, and a CLI node list
- Server-hosted installers and binary downloads
- Cross-compilation and parser/API tests

Build support is not the same as hardware certification. The current automated
tests do not validate every operating-system, driver, GPU vendor, or hardware
combination.

## Explicitly out of scope

- Background node agent, heartbeat, or continuous metrics collection
- Node removal, revocation, approval, hardware attestation, or durable identity
- GPU health qualification, benchmarking, scheduling, reservation, or jobs
- Remote execution, model serving, and LLM inference
- Agent framework integration
- Containers or distributed execution
- Production authentication/authorization, TLS termination, database storage,
  high availability, and multi-server consistency
- Signed binaries, checksums, automatic upgrades, and rollback

## Architecture

```text
OS tools and driver interfaces
  ├─ nvidia-smi
  ├─ rocm-smi / rocm_smi.py
  ├─ Linux DRM sysfs + lspci
  ├─ Windows Win32_VideoController through PowerShell CIM
  └─ macOS system_profiler
              ↓
      normalized gpu.Inventory
              ↓
  houdry gpu detect (local output)
              or
  houdry gpu join (HTTP snapshot)
              ↓
       Houdry Phase 1 server
```

All available detectors are attempted. Records are merged when they share a GPU
UUID or a normalized PCI bus ID. If neither identifier is available, records
from different sources may not be merged.

## Normalized inventory

An inventory contains:

- stable local node ID, detection timestamp, hostname, OS, architecture, and
  kernel where `uname -r` is available
- zero or more GPUs
- detector source names and non-fatal warnings

A GPU can contain:

- index, Houdry ID, vendor, and model
- UUID and PCI bus ID
- total and used memory
- driver version, CUDA compatibility version, and CUDA compute capability
- GPU utilization, memory utilization, and temperature
- the source(s) that populated the record

Most fields are optional. Zero or omitted values mean “not reported,” not zero
physical capacity or activity.

## Detector behavior and limitations

### NVIDIA (`nvidia-smi`)

Attempted on every operating system when `nvidia-smi` is found. Houdry queries
index, UUID, name, driver, total/used memory, GPU/memory utilization,
temperature, PCI bus ID, and compute capability. Older drivers that reject the
`compute_cap` field are retried without it. A separate default `nvidia-smi`
invocation is parsed for the reported CUDA compatibility version.

These values describe what the installed NVIDIA driver reports. They do not
guarantee that a CUDA toolkit or a particular model runtime is installed.

### AMD ROCm (`rocm-smi` or `rocm_smi.py`)

Attempted when either executable is present. Houdry requests product name, VRAM,
GPU use, temperature, and PCI bus information in JSON. Output-key variation
between ROCm versions can result in partial records.

### Linux

Houdry scans `/sys/class/drm/card*/device`. It can obtain PCI vendor/device IDs,
PCI bus ID, kernel driver-module name, and AMD VRAM files where exposed. It also
runs `lspci -nn -D`, when installed, to improve model names and identify display
controllers.

Linux sysfs fallback does not currently read live GPU utilization or
temperature. The “driver version” from sysfs is the kernel module name, not a
package version.

### Windows

Houdry invokes PowerShell (`powershell` or `pwsh`) and queries
`Win32_VideoController` with `Get-CimInstance`. This fallback supplies model,
vendor, PNP-derived identity, reported `AdapterRAM`, and driver version.

WMI/CIM adapter memory can be absent or inaccurate on some systems. Live
utilization, memory use, and temperature require a vendor-specific detector
such as `nvidia-smi`; the WMI path does not provide them.

### macOS

Houdry invokes:

```text
system_profiler SPDisplaysDataType -json
```

It extracts model, vendor, GPU core count when reported, and dedicated VRAM
when reported. The current parser intentionally treats “shared” or “dynamic”
memory as unknown, so Apple unified memory is not reported as GPU memory. This
path does not provide live utilization, temperature, or driver version.

## Installation

The server-hosted installers are available only while `houdry serve` is
reachable:

```bash
# Linux or macOS
curl -fsSL http://SERVER:8080/install.sh | sh

# Windows PowerShell
irm http://SERVER:8080/install.ps1 | iex
```

Git Bash, MSYS2, and Cygwin can use `install.sh`; it maps those environments to
the Windows binary. Native PowerShell should use `install.ps1`.

They download `/download/{os}/{arch}`, install into `~/.houdry/bin` (normally
`%USERPROFILE%\.houdry\bin` on Windows), write the server URL to
`~/.houdry/config.json`, and add the bin directory to the user PATH/profile.
Running a server-hosted installer rewrites that config file and resets the local
node ID.

The server can always serve its own executable for its own OS/architecture.
Other targets require matching files in the `--binaries` directory, normally
created with `make dist`.

The repository includes a tag-triggered GitHub Actions release workflow and
separate release installer scripts. No public release should be documented as
available until a release actually exists and its assets are publicly
downloadable.

## Join resolution and stored state

`gpu join` resolves its server and token in this order:

1. `--server` / `--token`
2. `HOODRY_SERVER` / `HOODRY_TOKEN`
3. `~/.houdry/config.json`

It runs detection immediately before sending `POST /v1/nodes/join`. The server
upserts by client-supplied node ID and records the request IP, first join time,
and latest join time. Running `gpu join` again replaces the inventory and
updates `last_seen`; no heartbeat runs between joins. A node with zero detected
GPUs is still accepted and stored.

The server keeps nodes in memory and writes the full set to
`<data-dir>/nodes.json` after an upsert. It loads that file on startup. The
current join handler does not report file-save failures to the client.

## HTTP API and security

- `GET /healthz` — public health/version response
- `GET /` — public dashboard
- `GET /install.sh` and `GET /install.ps1` — public installers
- `GET /download/{os}/{arch}` — public binary download
- `POST /v1/nodes/join` — token-protected only when a server token is set
- `GET /v1/nodes` — token-protected only when a server token is set

The token can be supplied as `X-Houdry-Token` or `Authorization: Bearer ...`.
There is one shared token and direct string comparison. The server uses plain
HTTP and trusts client-submitted inventory. It should be limited to a trusted
development network or placed behind correctly configured TLS and access
controls.

## Commands and flags

```bash
houdry gpu detect [--json]
houdry gpu join [--server URL] [--token TOKEN] [--json]
houdry gpu list [--server URL] [--token TOKEN] [--json]
houdry serve [--listen ADDR] [--data DIR] [--binaries DIR] [--token TOKEN]
houdry version
```

Defaults:

- listen address: `0.0.0.0:8080`
- data directory: `$HOODRY_HOME/server`, otherwise `~/.houdry/server`
- binaries directory: `dist`
- authentication: disabled unless `--token` or `HOODRY_TOKEN` is set
