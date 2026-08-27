# Houdry — private GPU fabric

Phase 1 discovers GPUs and can submit one inventory snapshot.
Phase 2 adds a persistent **node agent**, **heartbeat**, and **GPU jobs**.
Phase 3 adds **multi-node registration**, **resource profiles**, a **capability-aware scheduler**, **job queue**, and **drain/offline** handling.
Phase 4 adds a pluggable **Model Runtime** (Ollama first), **model discovery**, and **`inference` jobs**.
Phase 5 adds **intelligent model & resource routing** (task profile → catalog → best model+node).
Phase 6 adds an **OpenAI-compatible** `POST /v1/chat/completions` layer (for agents and SDKs) that still uses Houdry routing + scheduling.

## Two products (houdry-genomex)

| Product | Repo | Who |
|---------|------|-----|
| **Houdry** (this repo) | [houdry-genomex/houdry](https://github.com/houdry-genomex/houdry) | GPU / ops — `serve`, `node join`, models, `/v1` |
| **Houdry Agent** | [houdry-genomex/houdry-agent](https://github.com/houdry-genomex/houdry-agent) | End users — Desktop agent app |

Servers run the fabric. Normal users install **Houdry Agent** (a branded fork of
[Hermes Agent](https://github.com/NousResearch/hermes-agent) by Nous Research, MIT)
and point it at `http://<fabric-host>:18080/v1`. See the agent repo’s
[docs/HOUDRY.md](https://github.com/houdry-genomex/houdry-agent/blob/main/docs/HOUDRY.md).

The fabric stays **agent-framework agnostic**; Houdry Agent is the recommended
client, not a hard dependency.

- Phase 1 details: [docs/GPU-discovery.md](docs/GPU-discovery.md)
- Phase 2 details: [docs/Phase-2-node-agent.md](docs/Phase-2-node-agent.md)
- Phase 3 details: [docs/Phase-3-scheduling.md](docs/Phase-3-scheduling.md)
- Phase 4 details: [docs/Phase-4-model-runtime.md](docs/Phase-4-model-runtime.md)
- Phase 5 details: [docs/Phase-5-routing.md](docs/Phase-5-routing.md)
- OpenAI compat: [docs/OpenAI-compatible-API.md](docs/OpenAI-compatible-API.md)
- Public release / friend install: [docs/Release.md](docs/Release.md)

Repository: [houdry-genomex/houdry](https://github.com/houdry-genomex/houdry)

**Public install / release:** [docs/Release.md](docs/Release.md) (current line **v0.6.0**).

---

## For friends: install the current CLI (GPU / fabric hosts)

This installs the **GitHub latest release** binary into `~/.houdry/bin`. After
**v0.6.0** is published, that binary includes `node join`, jobs, routing, and
OpenAI-compatible chat — not the old Phase-1-only CLI.

### Linux / macOS

```bash
curl -fsSL https://github.com/houdry-genomex/houdry/releases/latest/download/install.sh | sh
export PATH="$HOME/.houdry/bin:$PATH"
houdry version
houdry gpu detect
# Join someone's fabric (Tailscale / LAN):
houdry node join --server http://<SERVER-IP>:18080
```

### Windows PowerShell

```powershell
irm https://github.com/houdry-genomex/houdry/releases/latest/download/install.ps1 | iex
& "$HOME\.houdry\bin\houdry.exe" version
& "$HOME\.houdry\bin\houdry.exe" gpu detect
& "$HOME\.houdry\bin\houdry.exe" node join --server http://<SERVER-IP>:18080
```

---

## Phase 2 milestone (one laptop)

Same APIs for local and remote — no special local mode.

```bash
# Terminal 1
make build
./bin/houdry serve --listen 0.0.0.0:18080

# Terminal 2
./bin/houdry node join --server http://127.0.0.1:18080

# Terminal 3
./bin/houdry job submit gpu.smoke --server http://127.0.0.1:18080 --wait
```

Open `http://127.0.0.1:18080/` for the cluster dashboard (READY / BUSY / DRAINING / OFFLINE).

---

## Phase 3 milestone (multi-node LAN)

```bash
# Machine A — control plane
./bin/houdry serve --listen 0.0.0.0:18080

# Machine B / C — workers (only need the server URL)
./bin/houdry node join --server http://<A-IP>:18080

# Any host — cluster view + VRAM-aware submit
./bin/houdry node list --server http://<A-IP>:18080
./bin/houdry job submit gpu.smoke --server http://<A-IP>:18080 --min-vram-mb 6000 --wait
```

---

## Phase 4 milestone (inference)

Requires [Ollama](https://ollama.com) on the worker (or another Model Runtime later).

```bash
ollama pull tinyllama   # small model for ~4 GB VRAM

./bin/houdry serve --listen 0.0.0.0:18080
./bin/houdry node join --server http://127.0.0.1:18080
./bin/houdry model list --server http://127.0.0.1:18080
./bin/houdry job submit inference \
  --server http://127.0.0.1:18080 \
  --model tinyllama \
  --prompt "Say hello from Houdry in one short sentence." \
  --wait
```

---

## Phase 5 milestone (routing)

```bash
./bin/houdry model catalog --server http://127.0.0.1:18080
./bin/houdry route --prompt "Say hello from Houdry" --server http://127.0.0.1:18080
./bin/houdry route --prompt "Say hello from Houdry" --execute --wait --server http://127.0.0.1:18080
./bin/houdry route --prompt "Refactor this Go function and add tests" --execute --wait --server http://127.0.0.1:18080
```

---

## Commands

```text
houdry gpu detect [--json]
houdry gpu join [--server URL] [--token TOKEN] [--json]
houdry gpu list [--server URL] [--token TOKEN] [--json]
houdry node join [--server URL] [--token TOKEN] [--interval DURATION]
houdry node list [--server URL] [--token TOKEN] [--json]
houdry node drain [--server URL] [--token TOKEN]
houdry node leave [--server URL] [--token TOKEN]
houdry model list [--server URL] [--token TOKEN] [--json]
houdry model catalog [--server URL] [--token TOKEN] [--json]
houdry route --prompt TEXT [--server URL] [--runtime NAME] [--require-model] [--execute] [--wait] [--json]
houdry job submit gpu.smoke [--server URL] [--min-vram-mb N] [--wait] [--json]
houdry job submit inference --model NAME --prompt TEXT [--server URL] [--runtime ollama] [--require-model] [--wait]
houdry job list [--server URL] [--token TOKEN] [--json]
houdry job get JOB_ID [--server URL] [--token TOKEN] [--json]
houdry job cancel JOB_ID [--server URL] [--token TOKEN] [--json]
houdry job clear --queued|--finished [--server URL] [--token TOKEN] [--json]
houdry serve [--listen ADDR] [--data DIR] [--binaries DIR] [--token TOKEN]
houdry version
```

Square brackets mean optional. Do not type `[` or `]`.

Environment: `HOODRY_SERVER`, `HOODRY_TOKEN`, `HOODRY_HOME`.

---

## Build

Requires Go 1.23+.

```bash
make build
make test
make dist          # cross-platform release assets → dist/
make dist-check    # assert version + node/job CLI surface
```

To publish: tag `v0.6.0` and push — see [docs/Release.md](docs/Release.md).
