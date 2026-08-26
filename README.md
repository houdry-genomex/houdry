# Houdry — Phase 1 GPU discovery and join

Houdry currently provides a cross-platform CLI that discovers GPUs on one
machine and can submit a one-time inventory snapshot to a Houdry server.
Scheduling, remote execution, model serving, agents, and continuous node
monitoring are not implemented.

See [the Phase 1 specification](docs/GPU-discovery.md) for exact behavior and
platform limitations.

## Current release status

There is not yet a published GitHub release. The public `curl`/PowerShell
installer URLs will not work until the repository is in its final GitHub
organization and a `v*` tag has produced a release. For now, build from source.

## Build and detect locally

Go 1.23 or newer is required.

```bash
make build
./bin/houdry gpu detect
```

Machine-readable output:

```bash
./bin/houdry gpu detect --json
```

`make build` builds for the current operating system and architecture.
`make dist` cross-compiles these six targets into `dist/`:

- Linux: amd64, arm64
- macOS: amd64, arm64
- Windows: amd64, arm64

Cross-compilation confirms that the code builds for a target; it is not a
substitute for testing on representative hardware and driver versions.

## Detect and join flow

Build all downloadable binaries and start the server:

```bash
make dist
./bin/houdry serve \
  --listen 0.0.0.0:8080 \
  --binaries dist \
  --token CHANGE_ME
```

Install on a Linux or macOS node:

```bash
curl -fsSL http://SERVER:8080/install.sh | sh
houdry gpu detect
HOODRY_TOKEN=CHANGE_ME houdry gpu join
```

Install on a Windows node from PowerShell:

```powershell
irm http://SERVER:8080/install.ps1 | iex
houdry gpu detect
$env:HOODRY_TOKEN = "CHANGE_ME"
houdry gpu join
```

The server-hosted installers save the server URL, so `gpu join` does not need
`--server`. If Houdry was installed another way:

```bash
houdry gpu join --server http://SERVER:8080 --token CHANGE_ME
```

Open `http://SERVER:8080/` for the dashboard, or list nodes from the CLI:

```bash
houdry gpu list --server http://SERVER:8080 --token CHANGE_ME
```

`gpu join` sends one snapshot. It does not start a daemon or heartbeat; values
and `last_seen` change only when the node runs `gpu join` again.

## Commands

- `houdry gpu detect [--json]`
- `houdry gpu join [--server URL] [--token TOKEN] [--json]`
- `houdry gpu list [--server URL] [--token TOKEN] [--json]`
- `houdry serve [--listen ADDR] [--data DIR] [--binaries DIR] [--token TOKEN]`
- `houdry version`

Configuration precedence for joins is command flag, environment variable, then
saved config. Supported environment variables are `HOODRY_SERVER`,
`HOODRY_TOKEN`, and `HOODRY_HOME`.

The default config path is `~/.houdry/config.json` (`%USERPROFILE%\.houdry\config.json`
on typical Windows installations). Server state defaults to
`~/.houdry/server/nodes.json`.

## Detection behavior

Houdry runs every detector available on the host and merges matching records by
GPU UUID or normalized PCI bus ID.

- NVIDIA (`nvidia-smi`, any supported OS): model, UUID, PCI ID, driver, total
  and used memory, GPU and memory utilization, temperature, CUDA compatibility
  version, and compute capability when the installed driver exposes it.
- AMD ROCm (`rocm-smi`/`rocm_smi.py`, when installed): model, PCI ID, VRAM,
  GPU utilization, temperature, and driver/ROCm version when present.
- Linux: DRM sysfs and optional `lspci` provide fallback identity, vendor, PCI
  ID, driver-module name, and VRAM fields where the kernel exposes them.
- Windows: `Win32_VideoController` through PowerShell CIM provides fallback
  model, vendor, reported adapter RAM, and driver version.
- macOS: `system_profiler SPDisplaysDataType -json` provides model, vendor, GPU
  core count where reported, and dedicated VRAM where reported. Unified/shared
  memory, live utilization, temperature, and driver version are not currently
  derived from `system_profiler`.

Missing tools or unsupported fields produce partial records or warnings rather
than invented values.

## Server and security limitations

- The server uses plain HTTP. Put TLS and access controls in front of it before
  exposing it beyond a trusted development network.
- `--token`/`HOODRY_TOKEN` is one shared bearer token. It protects only
  `POST /v1/nodes/join` and `GET /v1/nodes`.
- The dashboard, health endpoint, installers, and binary downloads remain
  unauthenticated even when a token is configured.
- Node inventory is held in memory and also written to `nodes.json`; this is a
  Phase 1 file store, not a production database.
- The server trusts inventory supplied by clients and does not establish node
  identity, attest hardware, encrypt stored inventory, or revoke nodes.

## Tests

```bash
make test
```

The automated tests cover parsing, normalization, persistence, API joins,
token checks, and installer rendering. Native hardware validation is still
required on macOS, Windows, Intel GPUs, and AMD/ROCm systems.
