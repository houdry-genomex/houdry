#!/usr/bin/env bash
# Creates the .venv that the CAD pipeline runs in. Safe to re-run.
# Usage: bash scripts/cad/setup.sh   (from the repo root)
set -euo pipefail

cd "$(dirname "$0")/../.."

if [ ! -d .venv ]; then
  echo "==> creating .venv"
  python -m venv .venv
fi

if [ -x .venv/Scripts/python.exe ]; then
  PY=.venv/Scripts/python.exe   # Windows
else
  PY=.venv/bin/python
fi

echo "==> installing cadquery (this pulls ~400 MB of OpenCascade, be patient)"
"$PY" -m pip install --quiet --upgrade pip
"$PY" -m pip install --quiet -r scripts/cad/requirements.txt

echo "==> verifying"
"$PY" -c "import cadquery; print('cadquery', cadquery.__version__, 'ready')"

cat <<'EOF'

Done. The CAD pipeline is ready.

Still required at runtime:
  1. Ollama running          -> https://ollama.com
  2. The two models pulled   -> ollama pull qwen2.5vl:7b
                                ollama pull llama3.1:8b
EOF
