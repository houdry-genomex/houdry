# Drawing → STEP CAD pipeline

Turns a 2D engineering drawing into a 3D solid model (STEP), fully on-device.
Wired into the routed chat: attaching an image whose prompt shows CAD intent
(see `cadIntent` in `internal/cli/route_cad.go`) runs this instead of plain
vision chat, and streams the pipeline log as chat deltas.

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

## Setup

The pipeline runs against a [cad3dify](https://github.com/neka-nat/cad3dify)
(MIT) checkout, expected as a sibling of this repo (override with
`HOUDRY_CAD3DIFY_DIR`):

```bash
git clone https://github.com/neka-nat/cad3dify.git ../cad3dify
cd ../cad3dify
python -m venv .venv && .venv/Scripts/pip install -e . cadquery
git apply /path/to/houdry/scripts/cad/cad3dify-ollama.patch   # adds Ollama provider
cp /path/to/houdry/scripts/cad/houdry_pipeline.py scripts/
```

`cad3dify-ollama.patch` teaches cad3dify an `ollama` model type pointing at the
local OpenAI-compatible endpoint; `houdry_pipeline.py` is the two-model driver
and does not depend on cad3dify's own chains.

## Run standalone

```bash
.venv/Scripts/python scripts/houdry_pipeline.py drawing.jpg \
  --output_filepath model.step
```

Env: `CAD3DIFY_OLLAMA_BASE` (default `http://127.0.0.1:11434`),
`CAD3DIFY_OLLAMA_MODEL` (vision), `CAD3DIFY_CODE_MODEL` (code).
