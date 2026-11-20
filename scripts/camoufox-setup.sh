#!/usr/bin/env bash
# Install the Camoufox backend's runtime dependencies on a Linux host:
#   - Xvfb (virtual display for headless="virtual")
#   - x11vnc + noVNC/websockify (the human-handoff view onto that display)
#   - a Python venv with the pinned camoufox + playwright packages
#   - the Camoufox browser build (`camoufox fetch`)
#
# Version pinning contract (keep these aligned or the backend fails fast):
#   - PLAYWRIGHT_VERSION below must equal camoufox.PlaywrightVersion in
#     internal/browser/camoufox/version.go, and the playwright-go module version
#     in go.mod (v0.MMmm.patch encodes the Playwright version).
#   - CAMOUFOX_VERSION pulls a specific Playwright; if it differs from
#     PLAYWRIGHT_VERSION the sidecar's startup version check reports the skew.
#
# This runs on Linux only (Xvfb). On macOS the Camoufox backend is unavailable
# by design; use the chromium backend or run this in Docker (Dockerfile.camoufox).
set -euo pipefail

CAMOUFOX_VERSION="${CAMOUFOX_VERSION:-0.4.11}"
PLAYWRIGHT_VERSION="${PLAYWRIGHT_VERSION:-1.52.0}"
VENV="${CAMOUFOX_VENV:-.venv-camoufox}"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "camoufox-setup: Camoufox's virtual display needs Linux/Xvfb; got $(uname -s)." >&2
  echo "Run this inside the Linux container built from Dockerfile.camoufox instead." >&2
  exit 1
fi

if command -v apt-get >/dev/null 2>&1; then
  echo "== installing system packages (Xvfb, x11vnc, novnc, websockify) =="
  sudo apt-get update
  sudo apt-get install -y --no-install-recommends \
    xvfb x11vnc novnc websockify python3 python3-venv python3-pip \
    libgtk-3-0 libx11-xcb1 libdbus-glib-1-2 libasound2
else
  echo "camoufox-setup: no apt-get; install Xvfb, x11vnc, novnc/websockify and python3 manually." >&2
fi

echo "== creating python venv at ${VENV} =="
python3 -m venv "${VENV}"
# shellcheck disable=SC1091
source "${VENV}/bin/activate"
pip install --upgrade pip
pip install "camoufox[geoip]==${CAMOUFOX_VERSION}" "playwright==${PLAYWRIGHT_VERSION}"

echo "== fetching the Camoufox browser build =="
python3 -m camoufox fetch

echo "== done =="
echo "Activate with:  source ${VENV}/bin/activate"
echo "Then run agentbrowser with AGENTBROWSER_BACKEND=camoufox (python3 must be the venv's)."
