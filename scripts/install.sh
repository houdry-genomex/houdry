#!/bin/sh
# Houdry public installer — Linux, macOS, Git Bash on Windows
# Usage:
#   curl -fsSL https://github.com/houdry-genomex/houdry/releases/latest/download/install.sh | sh
set -e

REPO="${HOODRY_REPO:-houdry-genomex/houdry}"
VERSION="${HOODRY_VERSION:-latest}"

if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
  echo "error: curl or wget is required" >&2
  exit 1
fi

os=$(uname -s | tr 'A-Z' 'a-z')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *)
    echo "error: unsupported architecture: $arch" >&2
    exit 1
    ;;
esac
case "$os" in
  linux) ;;
  darwin) ;;
  mingw*|msys*|cygwin*) os=windows ;;
  *)
    echo "error: unsupported operating system: $os" >&2
    echo "On Windows PowerShell, run:" >&2
    echo "  irm https://github.com/${REPO}/releases/latest/download/install.ps1 | iex" >&2
    exit 1
    ;;
esac

ext=""
if [ "$os" = windows ]; then
  ext=".exe"
fi

asset="houdry-${os}-${arch}${ext}"
if [ "$VERSION" = latest ]; then
  url="https://github.com/${REPO}/releases/latest/download/${asset}"
else
  url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
fi

echo "Downloading Houdry (${VERSION}) for ${os}/${arch}"
echo "  ${url}"

tmp=$(mktemp 2>/dev/null || mktemp -t houdry)
cleanup() { rm -f "$tmp"; }
trap cleanup EXIT

if command -v curl >/dev/null 2>&1; then
  curl -fL --silent --show-error \
    --retry 3 --retry-delay 2 --retry-max-time 180 \
    --connect-timeout 15 --max-time 180 \
    "$url" -o "$tmp"
else
  wget -qO "$tmp" "$url"
fi
echo "Download complete."

if [ ! -s "$tmp" ]; then
  echo "error: download was empty" >&2
  exit 1
fi
# GitHub 404 pages are HTML; a real binary is not.
if head -c 16 "$tmp" | grep -q '<html\|<HTML\|Not Found'; then
  echo "error: GitHub returned a web page, not a binary. Publish a release first." >&2
  exit 1
fi

houdry_home="${HOODRY_HOME:-${HOME}/.houdry}"
bin_dir="${houdry_home}/bin"
mkdir -p "$bin_dir"
dest="${bin_dir}/houdry${ext}"
mv "$tmp" "$dest"
trap - EXIT
chmod +x "$dest"

mkdir -p "$houdry_home"
if [ ! -f "${houdry_home}/config.json" ]; then
  cat > "${houdry_home}/config.json" <<EOF
{
  "server": "${HOODRY_SERVER:-}",
  "node_id": ""
}
EOF
fi

path_line="export PATH=\"${bin_dir}:\$PATH\""
add_path() {
  file="$1"
  if [ -f "$file" ] && grep -q '.houdry/bin' "$file" 2>/dev/null; then
    return 0
  fi
  if [ -w "$(dirname "$file")" ] || [ -f "$file" ]; then
    printf '\n# Houdry\n%s\n' "$path_line" >> "$file"
  fi
}

case "$os" in
  darwin)
    add_path "${HOME}/.zprofile"
    add_path "${HOME}/.zshrc"
    add_path "${HOME}/.bash_profile"
    ;;
  linux)
    add_path "${HOME}/.profile"
    add_path "${HOME}/.bashrc"
    add_path "${HOME}/.zshrc"
    ;;
esac

export PATH="${bin_dir}:${PATH}"

echo
echo "Houdry installed to ${dest}"
echo
echo "Check the version (should be current release, not Phase-1-only):"
echo "  houdry version"
echo
echo "Detect GPUs on this machine:"
echo "  houdry gpu detect"
echo
echo "Join a Houdry fabric as a node agent:"
echo "  houdry node join --server http://<server-ip>:18080"
echo
if [ -n "${HOODRY_SERVER:-}" ]; then
  echo "Join this fabric (HOODRY_SERVER is set):"
  echo "  houdry node join --server ${HOODRY_SERVER}"
  echo
fi
echo "If 'houdry' is not found, open a new terminal or run:"
echo "  export PATH=\"${bin_dir}:\$PATH\""
