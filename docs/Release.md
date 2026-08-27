# Releasing Houdry

Houdry ships a **single multi-platform CLI** via GitHub Releases. Friends install
that binary with the existing public installers — they do **not** need to clone
the repo.

Current release line: **v0.6.0** (Phases 1–6: GPU fabric, node agent, scheduling,
model runtime, routing, OpenAI-compatible chat completions).

**End-user Desktop agent** is a separate product:
[houdry-genomex/houdry-agent](https://github.com/houdry-genomex/houdry-agent)
(Houdry Agent Desktop, based on Hermes). Fabric releases here; agent Desktop
installers are published from that repo.

## What friends run

### GPU / fabric host (this repo)

### Linux / macOS

```bash
curl -fsSL https://github.com/houdry-genomex/houdry/releases/latest/download/install.sh | sh
export PATH="$HOME/.houdry/bin:$PATH"
houdry version
houdry node join --server http://<TAILSCALE-OR-LAN-IP>:18080
```

### Windows PowerShell

```powershell
irm https://github.com/houdry-genomex/houdry/releases/latest/download/install.ps1 | iex
& "$HOME\.houdry\bin\houdry.exe" version
& "$HOME\.houdry\bin\houdry.exe" node join --server http://<TAILSCALE-OR-LAN-IP>:18080
```

`latest` always points at the newest published tag. Pin a version with:

```bash
HOODRY_VERSION=v0.6.0 curl -fsSL \
  https://github.com/houdry-genomex/houdry/releases/download/v0.6.0/install.sh | sh
```

(or set `$env:HOODRY_VERSION = "v0.6.0"` before `install.ps1`).

## Release artifacts (must match installers)

Built by `make dist` / `.github/workflows/release.yml`:

| Asset | Used by |
|-------|---------|
| `houdry-linux-amd64` | `install.sh` on Linux x86_64 |
| `houdry-linux-arm64` | `install.sh` on Linux arm64 |
| `houdry-darwin-amd64` | `install.sh` on Intel macOS |
| `houdry-darwin-arm64` | `install.sh` on Apple Silicon |
| `houdry-windows-amd64.exe` | `install.ps1` / Git Bash on Windows x64 |
| `houdry-windows-arm64.exe` | Windows arm64 |
| `scripts/install.sh` | public curl installer |
| `scripts/install.ps1` | public PowerShell installer |

Architecture mapping is in the installers (`x86_64`/`amd64` → `amd64`,
`aarch64`/`arm64` → `arm64`). Do not rename assets without updating both
installers and the workflow `files:` list.

## How a release is published

The existing mechanism (do not invent another):

1. Land the intended code on `main`.
2. Tag and push:

```bash
git tag v0.6.0
git push origin v0.6.0
```

3. GitHub Actions workflow **Release** (`.github/workflows/release.yml`) runs
   `make test`, `make dist VERSION=0.6.0`, verifies `node join` / `node list` /
   `job submit` on the Linux amd64 binary, then attaches all assets to the
   GitHub Release for that tag.

4. Confirm on
   https://github.com/houdry-genomex/houdry/releases/tag/v0.6.0
   that assets include the six binaries plus both installers.

5. On a clean machine, run the curl/irm installers above and check:

```bash
houdry version   # → houdry 0.6.0
houdry help      # mentions node join / job submit
```

## Local verification (before tagging)

```bash
make test
make dist-check VERSION=0.6.0
./bin/houdry version
```

`VERSION` is stamped into the binary via `-ldflags` (see `Makefile`). The source
default in `internal/version/version.go` should match the release line.

## Server-hosted installers

While `houdry serve --binaries dist` is running, clients can also install from
the control plane:

```bash
curl -fsSL http://<server>:18080/install.sh | sh
```

Those scripts download `/download/{os}/{arch}` from that server (same OS/arch
asset names as the GitHub release).
