# Houdry — Phase 1 GPU discovery and join

Houdry discovers GPUs on a machine and can submit one inventory snapshot to a
Houdry server. Scheduling, remote execution, model serving, agents, and
continuous monitoring are not implemented yet.

Full Phase 1 details: [docs/GPU-discovery.md](docs/GPU-discovery.md)

Repository: [houdry-genomex/houdry](https://github.com/houdry-genomex/houdry)

---

## For friends: detect GPUs on your laptop

### Linux / macOS

```bash
curl -fsSL https://github.com/houdry-genomex/houdry/releases/latest/download/install.sh | sh
export PATH="$HOME/.houdry/bin:$PATH"
houdry gpu detect
```

### Windows PowerShell

```powershell
irm https://github.com/houdry-genomex/houdry/releases/latest/download/install.ps1 | iex
& "$HOME\.houdry\bin\houdry.exe" gpu detect
```

Notes:

- Install puts the binary in `~/.houdry/bin` (Windows: `%USERPROFILE%\.houdry\bin`).
- The current shell may not see `houdry` until you export `PATH` or open a new terminal.
- `gpu detect` only inspects the local machine. It does not send data to a server.

---

## Join a Houdry server (optional)

Someone must run the server first:

```bash
# from a machine that has the source / dist binaries
make dist
./bin/houdry serve --listen 0.0.0.0:18080 --binaries dist
```

Then each GPU machine:

```bash
export PATH="$HOME/.houdry/bin:$PATH"
houdry gpu join --server http://HOST:18080
houdry gpu list --server http://HOST:18080
```

Replace `HOST` with the real server IP or hostname (for example `127.0.0.1` on
the same machine, or `192.168.1.20` on a LAN). Do not type the word `HOST`
literally.

With an optional shared token:

```bash
./bin/houdry serve --listen 0.0.0.0:18080 --binaries dist --token CHANGE_ME
houdry gpu join --server http://HOST:18080 --token CHANGE_ME
```

Open `http://HOST:18080/` for the dashboard.

Server-hosted install (while the server is running):

```bash
# Linux / macOS
curl -fsSL http://HOST:18080/install.sh | sh

# Windows PowerShell
irm http://HOST:18080/install.ps1 | iex
```

Those installers save the server URL into `~/.houdry/config.json`, so
`houdry gpu join` can omit `--server`.

---

## Build from source

Requires Go 1.23+.

```bash
make build
./bin/houdry gpu detect

make dist    # linux/darwin/windows × amd64/arm64 into dist/
make test
```

---

## Commands

```text
houdry gpu detect [--json]
houdry gpu join [--server URL] [--token TOKEN] [--json]
houdry gpu list [--server URL] [--token TOKEN] [--json]
houdry serve [--listen ADDR] [--data DIR] [--binaries DIR] [--token TOKEN]
houdry version
```

In command help, square brackets mean optional flags. Do not type `[` or `]`.

Environment variables: `HOODRY_SERVER`, `HOODRY_TOKEN`, `HOODRY_HOME`.

Config: `~/.houdry/config.json` (Windows: `%USERPROFILE%\.houdry\config.json`).

Server state default: `~/.houdry/server/nodes.json`.

---

## Detection behavior

Houdry runs every available detector and merges matches by GPU UUID or PCI ID.

- NVIDIA (`nvidia-smi`): model, UUID, PCI, driver, memory, utilization,
  temperature, CUDA compatibility, compute capability when available
- AMD ROCm (`rocm-smi`): model, PCI, VRAM, utilization, temperature when available
- Linux: DRM sysfs + optional `lspci`
- Windows: `Win32_VideoController` via PowerShell CIM
- macOS: `system_profiler SPDisplaysDataType -json`

Missing tools produce partial results or warnings, not invented values.

---

## Security limitations (Phase 1)

- Plain HTTP only
- Optional shared token protects only `/v1/nodes/join` and `/v1/nodes`
- Dashboard, installers, and downloads stay public even with a token
- Client-submitted inventory is trusted
- File store, not a production database

---

## Release

Current release: [v0.1.0](https://github.com/houdry-genomex/houdry/releases/tag/v0.1.0)

Assets: Linux/macOS/Windows binaries (amd64 + arm64), `install.sh`, `install.ps1`.
