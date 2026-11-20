# Camoufox backend

chromancer can drive **Camoufox** — an anti-detect Firefox — as an alternative
to the default go-rod/Chromium backend, for targets whose bot detection blocks
headless Chrome. Camoufox runs in a virtual display (Xvfb) behind a Python
Playwright server; chromancer talks to it over Playwright (playwright-go).

Pick the backend with `AGENTBROWSER_BACKEND=chromium|camoufox` (default
`chromium`), or pin specific hosts with
`AGENTBROWSER_BACKEND_TARGETS="shop.example.com=camoufox,api.example.com=chromium"`.

## When to pick which

| | chromium (default) | camoufox |
|---|---|---|
| Startup | fast, no sidecar | slower (Python sidecar + Xvfb) |
| Platform | anywhere Chrome runs | **Linux only** (needs Xvfb) |
| Fingerprint | de-headlessed Chrome UA + client hints | Camoufox's own consistent Firefox fingerprint |
| UA override | yes (`AGENTBROWSER_UA`) | **no** — Camoufox keeps its fingerprint consistent |
| Session reuse | persistent profile dir | cookies + storage state restored per context (no persistent profile) |
| Best for | most sites | sites that block headless Chrome / fingerprint-gate |

Start with chromium. Move a specific target to camoufox only when it is blocked
on chromium and reaches the login form on camoufox from the same IP.

## Setup

### Local (Linux)

```sh
scripts/camoufox-setup.sh          # Xvfb, x11vnc, noVNC, venv with camoufox+playwright, camoufox fetch
source .venv-camoufox/bin/activate # so python3 can import camoufox
AGENTBROWSER_BACKEND=camoufox agentbrowser auth --account NAME --login-url URL
```

### Docker

```sh
docker build -f Dockerfile.camoufox -t agentbrowser-camoufox .
docker run --rm -e AGENTBROWSER_KEY agentbrowser-camoufox auth --account NAME --login-url URL
```

macOS/Windows have no Xvfb, so run the camoufox backend inside the Linux
container. On a host without Xvfb the backend is unavailable by design; the
chromium backend still works everywhere.

## Version pinning (must stay aligned)

The Playwright major.minor must match across three places, or the sidecar's
startup check fails with a clear message rather than mis-handshaking:

- `internal/browser/camoufox/version.go` → `PlaywrightVersion`
- the `playwright-go` module version in `go.mod` (its `v0.MMmm.patch` encodes the
  Playwright version — e.g. `v0.5200.0` ↔ Playwright `1.52.0`)
- `PLAYWRIGHT_VERSION` / `CAMOUFOX_VERSION` in `scripts/camoufox-setup.sh` and
  `Dockerfile.camoufox` (the camoufox package pulls a specific Playwright)

The sidecar reports its Playwright version in the ready line; `CheckPlaywrightVersion`
rejects a major/minor skew at startup.

## One server per account (stable fingerprint)

Each account gets its own Camoufox Playwright server, so its fingerprint stays
stable across runs. The fingerprint/config is persisted at
`<workspace>/accounts/<account>/camoufox.json` and reused on later runs; it is
never regenerated for an existing account unless the proxy **region** changes
(keeping a US timezone/locale against an EU exit IP is a worse tell than a fresh
consistent fingerprint — chromancer warns and regenerates in that case).

## Virtual display + human-handoff view

When a login needs a human (CAPTCHA chain exhausted, an unusual challenge), the
account's Xvfb display is exposed read/write through `x11vnc` + noVNC, behind a
small loopback reverse proxy. That proxy requires the one-time handoff token on
**every** request, including the WebSocket upgrade the VNC client opens (token in
the query string, an `X-Handoff-Token` header, or a `handoff.<token>` WebSocket
subprotocol). `needs_human.view_url` points at this proxy. Without a valid token
every request gets a flat `401` and never reaches the VNC upstream.

## Known differences from chromium

- **No persistent profile.** State is exported (cookies + local/sessionStorage)
  and re-imported into a fresh context each run; `sessionStorage` is seeded via a
  per-origin init script before page scripts run. Replayability of the captured
  session bundle is unchanged (same `RequestEvent`/`ResponseEvent` capture).
- **No UA override.** `AGENTBROWSER_UA` is ignored on camoufox; the backend
  advertises `SupportsUAOverride = false` so nothing tries to override Emulation
  and break the fingerprint.
- **Network bodies** are read as each response finishes; redirect bodies are
  skipped and oversized bodies capped, and a "body unavailable" never aborts the
  recording.

## Bot-detection comparison (live, optional)

A `live`-tagged check loads a public bot-detection test page on both backends
from the same IP and records the outcome here. It is not a pass/fail gate; it is
how we decide whether a given target is worth pinning to camoufox.

| Date | Site | chromium | camoufox | Notes |
|------|------|----------|----------|-------|
| _(pending a live run on a Linux host with Xvfb)_ | | | | |
