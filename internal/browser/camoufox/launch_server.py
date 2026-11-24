#!/usr/bin/env python3
"""Camoufox Playwright-server sidecar.

Launches ONE Camoufox (anti-detect Firefox) Playwright server in a virtual
display (Xvfb) and prints a single JSON "ready" line on stdout once the server
is actually accepting connections:

    {"ws": "ws://127.0.0.1:PORT/camoufox", "display": ":99",
     "playwright_version": "1.61.0", "camoufox_version": "0.5.9"}

The Go parent reads that line, connects with playwright-go (Firefox.Connect),
and keeps the process alive for the account's session.

Camoufox's `launch_server(...)` BLOCKS (it runs the Playwright server in the
foreground and prints its own ws endpoint), so it cannot return a handle. We
therefore run it in a daemon thread on a FIXED port the Go parent chose
(CAMOUFOX_PORT), wait for that port to accept TCP, then announce the ws endpoint
ourselves — no parsing of Camoufox's coloured stdout, no connect-before-listen
race.

One server per account gives that account a STABLE fingerprint: the
fingerprint/config is read from (and, on first run, written to) CAMOUFOX_FINGERPRINT.

Config comes from environment variables (never argv, so proxy credentials never
show up in the process list):

    CAMOUFOX_HOST          bind host (default 127.0.0.1)
    CAMOUFOX_PORT          bind port (REQUIRED; the Go parent picks a free one)
    CAMOUFOX_PROXY         upstream proxy URL (optional)
    CAMOUFOX_GEOIP         "1" to match geoip/timezone/locale to the proxy exit
    CAMOUFOX_FINGERPRINT   path to the persisted fingerprint/config JSON
    CAMOUFOX_REGION        region tag recorded alongside a freshly generated fingerprint

Runs only where Xvfb + the camoufox package are installed (Linux; see
scripts/camoufox-setup.sh and Dockerfile.camoufox).
"""
import importlib.metadata
import json
import os
import socket
import sys
import threading
import time


def _pkg_version(pkg):
    """Installed package version string, or "" — resolved via distribution
    metadata because playwright has no __version__ and camoufox's is a submodule."""
    try:
        return importlib.metadata.version(pkg)
    except Exception:
        return ""


def _load_fingerprint(path):
    if path and os.path.exists(path):
        try:
            with open(path) as fh:
                return json.load(fh).get("fingerprint")
        except Exception as exc:
            print(f"camoufox: could not read fingerprint {path}: {exc}", file=sys.stderr)
    return None


def _save_fingerprint(path, fp, region):
    if not path:
        return
    try:
        d = os.path.dirname(path)
        if d:
            os.makedirs(d, exist_ok=True)
        tmp = path + ".tmp"
        with open(tmp, "w") as fh:
            json.dump({"region": region, "fingerprint": fp}, fh)
        os.replace(tmp, path)
    except Exception as exc:
        print(f"camoufox: could not save fingerprint {path}: {exc}", file=sys.stderr)


def _port_open(host, port):
    # create_connection handles both IPv4 (127.0.0.1) and IPv6 (::1) loopback.
    try:
        with socket.create_connection((host, port), timeout=0.25):
            return True
    except OSError:
        return False


def main():
    host = os.environ.get("CAMOUFOX_HOST", "127.0.0.1")
    port = int(os.environ.get("CAMOUFOX_PORT", "0"))
    proxy_url = os.environ.get("CAMOUFOX_PROXY", "")
    geoip = os.environ.get("CAMOUFOX_GEOIP", "") == "1"
    fp_path = os.environ.get("CAMOUFOX_FINGERPRINT", "")
    region = os.environ.get("CAMOUFOX_REGION", "")

    if port == 0:
        print(json.dumps({"error": "CAMOUFOX_PORT is required (the parent must pick a free port)"}), flush=True)
        sys.exit(2)

    try:
        from camoufox.server import launch_server
        from camoufox.utils import generate_fingerprint
    except Exception as exc:
        print(json.dumps({"error": f"camoufox/playwright not installed: {exc}"}), flush=True)
        sys.exit(3)

    # Render into a provided display when there is one. The human-handoff path
    # exports DISPLAY=:99 with its own Xvfb so x11vnc/noVNC can watch the browser;
    # Camoufox's headless="virtual" would instead start its OWN, separate Xvfb that
    # x11vnc cannot see — leaving the handoff view BLACK. So: if DISPLAY is set,
    # render into it with headless=False (and report that display); otherwise fall
    # back to "virtual" (Camoufox manages Xvfb itself). Either way the browser is
    # off-screen in a virtual framebuffer — this only controls WHICH one.
    display = os.environ.get("DISPLAY", "").strip()
    headless_mode = False if display else "virtual"

    # Bind the Playwright server to IPv4 loopback explicitly: Camoufox/Playwright
    # otherwise resolves "localhost" to IPv6 (::1), which the Go client's endpoint
    # and the readiness poll may not match.
    kwargs = dict(headless=headless_mode, host=host, port=port, ws_path="camoufox")
    if proxy_url:
        kwargs["proxy"] = {"server": proxy_url}
        kwargs["geoip"] = True  # match geoip/timezone/locale to the proxy exit
    elif geoip:
        kwargs["geoip"] = True

    # Stable per-account fingerprint: reuse the saved one; on first run generate
    # one and persist it so later runs for this account look identical, while two
    # different accounts get independent fingerprints.
    fp = _load_fingerprint(fp_path)
    if fp is None:
        fp = generate_fingerprint()
        _save_fingerprint(fp_path, fp, region)
    kwargs["fingerprint"] = fp

    # launch_server blocks; run it in a daemon thread and capture a startup error.
    err = {}

    def _run():
        try:
            launch_server(**kwargs)
        except Exception as exc:  # noqa: BLE001
            err["e"] = exc

    threading.Thread(target=_run, daemon=True).start()

    # Wait until the server is actually accepting connections (or it failed).
    # Prefer the IPv4 host we asked for, but fall back to ::1 in case Playwright
    # bound IPv6 loopback regardless, and report whichever actually listens.
    ws = None
    for _ in range(600):  # up to ~60s
        if "e" in err:
            break
        if _port_open(host, port):
            ws = f"ws://{host}:{port}/camoufox"
            break
        if _port_open("::1", port):
            ws = f"ws://[::1]:{port}/camoufox"
            break
        time.sleep(0.1)

    if ws is None:
        reason = err.get("e", "timed out waiting for the server port")
        print(json.dumps({"error": f"camoufox server did not start: {reason}"}), flush=True)
        sys.exit(4)

    print(json.dumps({
        "ws": ws,
        "display": os.environ.get("DISPLAY", ""),
        "playwright_version": _pkg_version("playwright"),
        "camoufox_version": _pkg_version("camoufox"),
    }), flush=True)

    # Keep the server alive until the parent kills the process group.
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
