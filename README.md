# Houdry — Phase 1 GPU fabric

Private GPU discovery and join. Same CLI on **Linux, macOS, and Windows**.

## Install (friends / any machine)

Linux and macOS:

```bash
curl -fsSL https://github.com/Abhishekmishra2808/houdry/releases/latest/download/install.sh | sh
houdry gpu detect
```

Windows PowerShell:

```powershell
irm https://github.com/Abhishekmishra2808/houdry/releases/latest/download/install.ps1 | iex
houdry gpu detect
```

That only installs the CLI and detects local GPUs. It does not send anything to a server.

To also **join** a fabric, point at a running Houdry server:

```bash
houdry gpu join --server http://YOUR_SERVER:8080
```

## Private server flow

On a machine that should host the control plane:

```bash
houdry serve --listen 0.0.0.0:8080
```

On each GPU machine:

```text
1. curl  …          install the houdry binary
2. houdry gpu detect
3. houdry gpu join
4. node appears on the server
```

### Install (Linux and macOS)

```bash
curl -fsSL http://SERVER:8080/install.sh | sh
houdry gpu detect
houdry gpu join
```

### Install (Windows PowerShell)

Windows does not run `curl | sh`. The equivalent one-liner is:

```powershell
irm http://SERVER:8080/install.ps1 | iex
houdry gpu detect
houdry gpu join
```

`houdry gpu detect` and `houdry gpu join` are the same commands on all three operating systems.

If you already have the binary (for example after `make build`):

```bash
export HOODRY_SERVER=http://SERVER:8080
houdry gpu detect
houdry gpu join
```

Open `http://SERVER:8080/` to see joined nodes.

## Build

Requires Go 1.23+.

```bash
make build      # current OS/arch → bin/houdry
make dist       # linux/darwin/windows × amd64/arm64
make test
```

`houdry serve` serves install scripts and binaries. For other platforms to `curl` install, run `make dist` first and start the server from the repo (it looks in `./dist`).

```bash
make dist
./bin/houdry serve --listen 0.0.0.0:8080 --binaries dist
```

## Commands

| Command | Purpose |
|---|---|
| `houdry gpu detect [--json]` | Inspect local GPUs |
| `houdry gpu join [--server URL]` | Register this machine's GPUs with the server |
| `houdry gpu list [--server URL]` | Show nodes already joined |
| `houdry serve [--listen ADDR]` | Run the control plane |

Environment: `HOODRY_SERVER`, `HOODRY_TOKEN`, `HOODRY_HOME`.

Config is stored at `~/.houdry/config.json` on every OS (including Windows).

## GPU sources

| Platform | Sources |
|---|---|
| Linux | nvidia-smi, rocm-smi, sysfs DRM, lspci |
| Windows | nvidia-smi, Win32_VideoController (WMI) |
| macOS | nvidia-smi (Intel Macs), system_profiler / Apple Silicon |

Detectors produce one **normalized GPU model**. The rest of Houdry never talks to nvidia-smi or WMI directly.
