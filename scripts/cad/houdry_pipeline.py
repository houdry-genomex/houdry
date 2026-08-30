"""Houdry two-model CAD pipeline (fully local).

A small vision model can read a drawing but writes poor CadQuery; a code model
writes usable CadQuery but cannot see. So: qwen2.5-vl DESCRIBES the drawing
(shapes, dimensions, holes), llama3.1 WRITES the CadQuery code from that
description, and an execute-with-error-feedback loop repairs it. All calls go
to the loopback Ollama daemon; the only dependency is cadquery.

Inspired by cad3dify (MIT, neka-nat) but self-contained.

Usage:
  python houdry_pipeline.py <image> --output_filepath out.step
"""

import argparse
import base64
import io
import json
import os
import re
import sys
import urllib.error
import urllib.request

OLLAMA = os.environ.get("HOUDRY_OLLAMA_BASE", "http://127.0.0.1:11434")
VISION_MODEL = os.environ.get("HOUDRY_VISION_MODEL", "qwen2.5vl:7b")
CODE_MODEL = os.environ.get("HOUDRY_CODE_MODEL", "llama3.1:8b")

# Context the models are given. Large enough for a downscaled drawing plus the
# cheatsheet and a repair traceback; small enough to stay in VRAM alongside the
# two models this pipeline keeps warm.
NUM_CTX = int(os.environ.get("HOUDRY_NUM_CTX", "16384"))
# Long edge, in pixels, the drawing is resized down to before the vision pass.
MAX_IMAGE_EDGE = int(os.environ.get("HOUDRY_MAX_IMAGE_EDGE", "1280"))

DESCRIBE_PROMPT = (
    "You are reading a 2D engineering drawing. Describe the part precisely for a CAD engineer:\n"
    "1. Overall shape and outer dimensions (use the numbers printed on the drawing).\n"
    "2. Every hole/bore: diameter, depth (or through), and position.\n"
    "3. Steps, shoulders, chamfers, fillets with sizes.\n"
    "4. Which view is which (front/top/side).\n"
    "Only state what you can actually read. Use millimeters. Be compact and structured."
)

# The model must ONLY use APIs from this sheet — CadQuery is rare in training
# data and models otherwise invent methods (.holes, Holes, .eachPoint) that do
# not exist.
CADQUERY_CHEATSHEET = """CadQuery API you may use (nothing else):

import cadquery as cq

# Stacked cylinders (a shaft/flange): union solids built at known heights.
base = cq.Workplane("XY").circle(50.0).extrude(10.0)                          # disc dia 100, 0..10
boss = cq.Workplane("XY", origin=(0.0, 0.0, 10.0)).circle(20.0).extrude(15.0)  # dia 40, 10..25
step = cq.Workplane("XY", origin=(0.0, 0.0, 25.0)).circle(12.0).extrude(10.0)  # dia 24, 25..35
result = base.union(boss).union(step)

# Holes: NEVER use .faces()/.hole(). Build cutter cylinders on the base
# plane and boolean-subtract them — this always works:
cutters = (cq.Workplane("XY")
    .polarArray(radius=40.0, startAngle=0.0, angle=360.0, count=6)
    .circle(5.0)                     # hole RADIUS (= diameter / 2)
    .extrude(500.0, both=True))      # long both ways -> through everything
result = result.cut(cutters)

# One centered through-hole, same pattern:
center_cutter = cq.Workplane("XY").circle(3.0).extrude(500.0, both=True)
result = result.cut(center_cutter)

# Box: cq.Workplane("XY").box(30.0, 20.0, 10.0)
# Rectangle sketch: .rect(20.0, 10.0).extrude(5.0)

cq.exporters.export(result, "out.step")

RULES:
- NEVER call .fillet or .chamfer — ignore all fillets/chamfers on the drawing.
- NEVER call .faces(), .edges(), .workplane() or .hole() — cut holes with the
  cutter + result.cut(cutters) pattern shown above.
- Stack cylinders by extruding taller shapes first from the same "XY" plane,
  or extrude() again after another .circle() on cq.Workplane("XY", origin=(0, 0, height)).
- Methods that DO NOT exist: .holes, .eachPoint, .Holes, cq.Holes, .drill, .bore.
- Always floats in millimeters. circle() takes RADIUS.
- Build ONE solid named result; export as the last line."""

CODE_PROMPT = """{cheatsheet}

Model this part from an engineering drawing description:

{description}

Hard requirements:
- Build one solid in a variable named result.
- Last line must be exactly: cq.exporters.export(result, r"{out}")
- Output ONLY a python code block."""

FIX_PROMPT = """{cheatsheet}

This cadquery script failed with the error shown. Fix it using ONLY the APIs from the sheet above. Output the complete corrected script as ONLY a python code block. Keep the final line: cq.exporters.export(result, r"{out}")

Script:
```python
{code}
```

Error:
{error}"""


def ollama(model: str, prompt: str, images=None, timeout=1500) -> str:
    # num_ctx must be set explicitly. Ollama defaults to 4096, which nothing
    # here fits in: a drawing costs ~1-4k vision tokens on its own, and the
    # cheatsheet + description + a failing traceback comfortably exceed it on
    # the repair pass. Without this the daemon rejects the request outright
    # (400 exceed_context_size_error) rather than truncating.
    body = {"model": model, "prompt": prompt, "stream": False,
            "options": {"temperature": 0.2, "num_predict": 8192, "num_ctx": NUM_CTX},
            "keep_alive": "30m"}
    if images:
        body["images"] = images
    req = urllib.request.Request(
        OLLAMA + "/api/generate", data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read())["response"]
    except urllib.error.HTTPError as e:
        # urllib's default message is just "Bad Request"; the daemon puts the
        # actual reason in the body, so surface it or debugging is guesswork.
        raise RuntimeError(f"ollama {model} returned {e.code}: "
                           f"{e.read().decode('utf-8', 'replace')[:500]}") from None


def load_image_b64(path: str) -> str:
    """Read an image, downscaling it to something a vision model can afford.

    A phone photo or a 4K scan costs thousands of vision tokens and slows the
    describe pass by minutes, while adding no legible detail: dimension text on
    an engineering drawing is readable well below this cap. Pillow is already a
    cadquery dependency, so this costs no extra install; if it is somehow
    missing, the raw bytes are still better than failing.
    """
    try:
        from PIL import Image
    except ImportError:
        return base64.b64encode(open(path, "rb").read()).decode()

    with Image.open(path) as im:
        im = im.convert("RGB")
        if max(im.size) > MAX_IMAGE_EDGE:
            scale = MAX_IMAGE_EDGE / max(im.size)
            new_size = (max(1, round(im.width * scale)), max(1, round(im.height * scale)))
            print(f"[houdry-cad] downscaling {im.width}x{im.height} -> "
                  f"{new_size[0]}x{new_size[1]}", flush=True)
            im = im.resize(new_size, Image.LANCZOS)
        buf = io.BytesIO()
        im.save(buf, format="JPEG", quality=90)
        return base64.b64encode(buf.getvalue()).decode()


def coach(err: str) -> str:
    """Translate cryptic CadQuery failures into instructions a model can act on."""
    hints = []
    if "ParseException" in err:
        hints.append('Your face/edge selector string is invalid. Use exactly ">Z", "<Z", "|Z", ">X", "<X", ">Y" or "<Y" — nothing else, no "=" characters, no keyword arguments.')
    if "has no attribute" in err:
        hints.append("You called a method that does not exist in cadquery. Use ONLY methods shown in the cheat sheet.")
    if "No pending wires" in err:
        hints.append("You called extrude() without a 2D sketch on the stack. Sketch with .circle(r) or .rect(w,h) immediately before .extrude(h).")
    if "Cannot find a solid" in err:
        hints.append("hole()/fillet()/chamfer() need an existing solid. Build the base solid with circle().extrude() FIRST, then cut holes.")
    if "BRep_API" in err or "StdFail" in err:
        hints.append("A geometry operation failed. Make every hole diameter smaller than the face it cuts, keep each extrude positive, and put the workplane on a real face before cutting. Simplify the shape if needed.")
    if "requires that edges be selected" in err.lower():
        hints.append("Do not call .fillet or .chamfer at all — remove them.")
    if "co-planar" in err.lower():
        hints.append("Do not use .faces()/.workplane()/.hole() at all. Cut holes by building cutter cylinders on cq.Workplane(\"XY\") with .circle(r).extrude(500.0, both=True) and result.cut(cutters).")
    if not hints:
        return err
    return err + "\n\nHow to fix: " + " ".join(hints)


def strip_think(text: str) -> str:
    return re.sub(r"<think>.*?</think>", "", text, flags=re.S).strip()


def extract_code(text: str) -> str:
    text = strip_think(text)
    blocks = re.findall(r"```(?:python)?\s*(.*?)```", text, flags=re.S)
    code = max(blocks, key=len).strip() if blocks else text.strip()
    return sanitize(code)


def sanitize(code: str) -> str:
    """Strip edge-cosmetic calls: they are the #1 failure class for local
    models and are irrelevant to a dimensionally-correct demo model."""
    code = re.sub(r"\.edges\([^)]*\)\s*\.\s*(fillet|chamfer)\([^)]*\)", "", code)
    code = re.sub(r"\.(fillet|chamfer)\([^)]*\)", "", code)
    return code


def export_mesh(scope: dict, step_path: str) -> str:
    """Write an STL beside the STEP so viewers have something renderable.

    STEP is a b-rep exchange format — a browser cannot draw it without a
    geometry kernel. The chat previewer needs a triangle mesh, and OpenCascade
    is already loaded here, so tessellate once at export time rather than
    shipping a CAD kernel to the renderer.

    Best-effort: a missing STL costs the preview, not the STEP the user asked
    for, so failures are reported and swallowed.
    """
    stl_path = os.path.splitext(step_path)[0] + ".stl"
    try:
        result = scope.get("result")
        if result is None:
            print("[houdry-cad] no `result` in scope; skipping STL", flush=True)
            return ""
        import cadquery as cq

        # tolerance controls chord error; 0.1 mm is well under the precision
        # these drawings are dimensioned to and keeps the file small.
        cq.exporters.export(result, stl_path, tolerance=0.1)
        print(f"[houdry-cad] STL exported: {stl_path}", flush=True)
        return stl_path
    except Exception as e:  # noqa: BLE001 — preview is optional, STEP is not
        print(f"[houdry-cad] STL export skipped ({type(e).__name__}: {e})", flush=True)
        return ""


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("image")
    ap.add_argument("--output_filepath", default="output.step")
    ap.add_argument("--retries", type=int, default=4)
    args = ap.parse_args()

    out = os.path.abspath(args.output_filepath).replace(os.sep, "/")
    img_b64 = load_image_b64(args.image)

    print(f"[houdry-cad] describing drawing with {VISION_MODEL} ...", flush=True)
    description = strip_think(ollama(VISION_MODEL, DESCRIBE_PROMPT, images=[img_b64]))
    print("[houdry-cad] drawing understood:", flush=True)
    print("  " + description.replace("\n", "\n  ")[:1200], flush=True)

    print(f"[houdry-cad] writing CadQuery code with {CODE_MODEL} ...", flush=True)
    code = extract_code(ollama(CODE_MODEL, CODE_PROMPT.format(
        cheatsheet=CADQUERY_CHEATSHEET, description=description, out=out)))

    for attempt in range(1, args.retries + 1):
        print(f"[houdry-cad] executing attempt {attempt} ...", flush=True)
        scope: dict = {}
        try:
            exec(compile(code, "<generated>", "exec"), scope)  # noqa: S102 — that is the tool's job
            if os.path.exists(out) and os.path.getsize(out) > 0:
                print(f"[houdry-cad] STEP exported: {out}", flush=True)
                export_mesh(scope, out)
                return 0
            raise RuntimeError("script ran but exported no STEP file")
        except Exception as e:  # noqa: BLE001 — error text goes back to the model
            err = f"{type(e).__name__}: {e}"
            print(f"[houdry-cad] attempt {attempt} failed: {err}", flush=True)
            if attempt == args.retries:
                break
            # Fillet/chamfer are cosmetic: when their edge selector matches
            # nothing, drop them deterministically instead of spending a
            # multi-minute model round on a finished part.
            if "suitable edges for chamfer or fillet" in err:
                stripped = re.sub(r"\.(fillet|chamfer)\([^)]*\)", "", code)
                if stripped != code:
                    print("[houdry-cad] dropping fillet/chamfer (no matching edges); retrying without a model round ...", flush=True)
                    code = stripped
                    continue
            print(f"[houdry-cad] asking {CODE_MODEL} to repair the code ...", flush=True)
            code = extract_code(ollama(CODE_MODEL, FIX_PROMPT.format(
                cheatsheet=CADQUERY_CHEATSHEET, code=code, error=coach(err), out=out)))

    print("[houdry-cad] all attempts failed", flush=True)
    return 1


if __name__ == "__main__":
    sys.exit(main())
