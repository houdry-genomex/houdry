# Drawing → STEP CAD pipeline

Turns a 2D engineering drawing into a 3D solid model (STEP), fully on-device.
Wired into the routed chat: attaching an image whose prompt shows CAD intent
(see `cadIntent` in `internal/cli/route_cad.go`) runs this instead of plain
vision chat, and streams the pipeline log as chat deltas.

Self-contained — the only dependency is `cadquery`. The approach is inspired by
[cad3dify](https://github.com/neka-nat/cad3dify) (MIT, neka-nat), but no code or
checkout from it is required.

## Setup

```bash
bash scripts/cad/setup.sh      # creates .venv and installs cadquery
```

Then, at runtime, you also need:

1. **Ollama** running — https://ollama.com
2. **The two models pulled:**
   ```bash
   ollama pull qwen2.5vl:7b    # reads the drawing (~6 GB)
   ollama pull llama3.1:8b     # writes the CadQuery code (~4.9 GB)
   ```

No API keys, no network calls — inference is on Ollama over loopback and the
geometry kernel (OpenCascade) is local.

## Why two models

A small vision model can read a drawing but writes poor CadQuery; a code model
writes usable CadQuery but cannot see. So the pipeline splits the job:

1. `qwen2.5vl:7b` **describes** the drawing (shapes, dimensions, holes).
2. `llama3.1:8b` **writes** CadQuery code from that description.
3. An execute-with-error-feedback loop repairs failures (4 attempts), with
   `coach()` translating cryptic OpenCascade errors into actionable hints.

## The API-restriction trick

Local models hallucinate CadQuery APIs (`.holes`, `cq.Holes`, `.eachPoint`) and
misuse face selectors, which produced almost every failure we hit:
`Selected faces must be co-planar`, `requires that edges be selected`,
`ParseException`. `CADQUERY_CHEATSHEET` therefore bans `.faces()`, `.edges()`,
`.workplane()`, `.hole()`, `.fillet()` and `.chamfer()` outright and pins two
patterns that cannot fail that way:

- **Stacking** — union solids built at explicit heights via
  `cq.Workplane("XY", origin=(0, 0, z))`, never re-selecting a top face.
- **Holes** — cutter cylinders (`.circle(r).extrude(500.0, both=True)`) removed
  with `result.cut(cutters)`, never `.hole()`.

`sanitize()` also strips any fillet/chamfer calls that slip through.

Trade-off: models come out dimensionally correct but omit cosmetic fillets and
chamfers. That is deliberate — reliability over cosmetic edges.

## Run standalone

```bash
.venv/Scripts/python scripts/cad/houdry_pipeline.py drawing.jpg \
  --output_filepath model.step        # .venv/bin/python on Linux/macOS
```

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `HOUDRY_OLLAMA_BASE` | `http://127.0.0.1:11434` | Ollama endpoint |
| `HOUDRY_VISION_MODEL` | `qwen2.5vl:7b` | Reads the drawing |
| `HOUDRY_CODE_MODEL` | `llama3.1:8b` | Writes the CadQuery |
| `HOUDRY_CAD_PYTHON` | repo `.venv`, else PATH | Interpreter with cadquery |
| `HOUDRY_CAD_SCRIPT` | `scripts/cad/houdry_pipeline.py` | Pipeline location |
