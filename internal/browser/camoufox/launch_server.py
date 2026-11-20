#!/usr/bin/env python3
"""Camoufox Playwright-server sidecar.

Launches ONE Camoufox (anti-detect Firefox) Playwright server in a virtual
display (Xvfb) and prints a single JSON "ready" line on stdout:

    {"ws": "ws://127.0.0.1:PORT/...", "display": ":99", "playwright_version": "1.52.0"}

The Go parent reads that line, connects with playwright-go (Firefox.Connect),
and keeps the process alive for the account's session. One server per account
gives that account a STABLE fingerprint: the fingerprint/config is read from
(and, on first run, written to) the path in CAMOUFOX_FINGERPRINT.

Config comes from environment variables (never the command line, so secrets such
as proxy credentials never show up in the process list):

    CAMOUFOX_HOST          bind host (default 127.0.0.1)
    CAMOUFOX_PORT          bind port (default 0 = random free port)
    CAMOUFOX_PROXY         upstream proxy URL (optional)
    CAMOUFOX_GEOIP         "1" to match geoip/timezone/locale to the proxy exit
    CAMOUFOX_FINGERPRINT   path to the persisted fingerprint/config JSON
    CAMOUFOX_REGION        region tag recorded alongside a freshly generated fingerprint

NOTE: This runs only where Xvfb + the camoufox package are installed (Linux;
see scripts/camoufox-setup.sh and Dockerfile.camoufox). It is exercised live in
that environment; on hosts without Xvfb the backend is unavailable by design.
"""
import json
import os
import sys
import time


def _load_fingerprint(path):
    if path and os.path.exists(path):
        try:
            with open(path) as fh:
                data = json.load(fh)
            return data.get("config")
        except Exception as exc:  # fall back to a fresh fingerprint
            print(f"camoufox: could not read fingerprint {path}: {exc}", file=sys.stderr)
    return None


def _save_fingerprint(path, config, region):
    if not path:
        return
    try:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        tmp = path + ".tmp"
        with open(tmp, "w") as fh:
            json.dump({"region": region, "config": config}, fh)
        os.replace(tmp, path)
    except Exception as exc:
        print(f"camoufox: could not save fingerprint {path}: {exc}", file=sys.stderr)


def main():
    host = os.environ.get("CAMOUFOX_HOST", "127.0.0.1")
    port = int(os.environ.get("CAMOUFOX_PORT", "0"))
    proxy_url = os.environ.get("CAMOUFOX_PROXY", "")
    geoip = os.environ.get("CAMOUFOX_GEOIP", "") == "1"
    fp_path = os.environ.get("CAMOUFOX_FINGERPRINT", "")
    region = os.environ.get("CAMOUFOX_REGION", "")

    try:
        from camoufox.server import launch_server
        import playwright
    except Exception as exc:
        print(json.dumps({"error": f"camoufox/playwright not installed: {exc}"}), flush=True)
        sys.exit(3)

    kwargs = dict(
        headless="virtual",   # Xvfb-backed virtual display
        port=port,
        ws_path="camoufox",
    )
    if proxy_url:
        kwargs["proxy"] = {"server": proxy_url}
        kwargs["geoip"] = geoip or True  # match geoip to the proxy exit
    elif geoip:
        kwargs["geoip"] = True

    saved = _load_fingerprint(fp_path)
    if saved is not None:
        # Reuse the account's stable fingerprint/config verbatim.
        kwargs["config"] = saved

    # launch_server starts the Playwright server and returns a handle exposing
    # the ws endpoint; it keeps running until the process is killed.
    server = launch_server(**kwargs)
    ws = getattr(server, "ws_endpoint", None) or getattr(server, "wsEndpoint", None)
    if ws is None and hasattr(server, "ws_endpoint_url"):
        ws = server.ws_endpoint_url

    if saved is None:
        # Persist the fingerprint Camoufox generated so later runs are stable.
        gen = getattr(server, "config", None)
        if gen is not None:
            _save_fingerprint(fp_path, gen, region)

    ready = {
        "ws": ws,
        "display": os.environ.get("DISPLAY", ""),
        "playwright_version": getattr(playwright, "__version__", ""),
    }
    print(json.dumps(ready), flush=True)

    # Keep the server alive until killed by the parent (process-group kill).
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
