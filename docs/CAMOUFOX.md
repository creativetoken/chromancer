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

- `internal/browser/camoufox/version.go` → `PlaywrightVersion` (currently `1.61.0`)
- the `playwright-go` module version in `go.mod` (`github.com/mxschmitt/playwright-go`
  `v0.6100.0`; its `v0.MMmm.patch` encodes the Playwright version — `v0.6100.0` ↔
  Playwright `1.61.0`)
- `PLAYWRIGHT_VERSION` / `CAMOUFOX_VERSION` in `scripts/camoufox-setup.sh` and
  `Dockerfile.camoufox` (the camoufox package pulls a specific Playwright)

**Validated version set (arm64 Linux):** `camoufox 0.5.9` (pip `camoufox[geoip]`),
`playwright 1.61.0` (camoufox 0.5.9 allows `playwright<1.63`; pinned to 1.61 to
match the Go client), `playwright-go v0.6100.0`, Camoufox browser build
`v158.0.1-beta.41` (`camoufox fetch`, ~1.3 GB). Note: camoufox 0.5.9 bundles
Playwright 1.62 by default, but playwright-go has no 1.62 client yet, so the
Python side is pinned down to 1.61.

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
- **Slower interaction.** Firefox-via-Playwright starts slower (sidecar + Xvfb)
  and each action takes longer than headless Chrome. One place this mattered: the
  agent-takeover verification step now gets its own budget (`agentVerifyBudget`,
  ≥45s) rather than inheriting the short escalation-trigger `VerifyTimeout`, so a
  code has time to arrive and be entered on the slower engine.
- **Coordinate click_at + custom event handlers / JS navigation.** A real mouse
  `ClickAt(x,y)` fires the click handler of a form button (incl. one in a closed
  shadow root — so the visual `shadow-login` login works on camoufox), but did
  NOT fire the handler of a standalone over-canvas control, and camoufox ignores
  JS-initiated top-level navigation (`location.href=`, `anchor.click()`,
  `form.submit()`). Diagnosed exhaustively (an Eval-injected listener fires where
  the page's own inline one does not). Impact: the `canvas-signin` visual lab is
  **chromium-only** (`TestE2EAgentPrimaryVisualApps/canvas-signin` skips on
  camoufox); visual perception itself is proven on camoufox by `shadow-login`
  (full login through a closed shadow root). **Follow-up:** root-cause the
  coordinate-click event dispatch in the playwright-go/camoufox path.
- Every other lab e2e flow that passes on chromium also passes on camoufox.

## Validation (arm64 Linux, Docker)

The full lab e2e suite was run with `AGENTBROWSER_E2E_BACKEND=camoufox`. The whole
suite passes on camoufox, matching chromium. Highlights proven live:

- cookie/JWT/SPA logins, modal and hidden-entry flows, signup+email-verify,
  magic-link, OTP, form-errors, the hydration race, and the agent-takeover
  scenarios (including resend-OTP) — all pass on camoufox.
- **Fingerprint stability**: two runs for one account yield an identical
  fingerprint; two accounts differ (`TestE2ECamoufoxFingerprintStability`).
- **Popups/new windows**: `TestE2EPopupLogin` passes on both backends.
- **Human-handoff view**: a live `x11vnc + websockify/noVNC` behind the token
  proxy serves the virtual display WITH the token (HTTP 200 / WS 101) and refuses
  every request WITHOUT it (HTTP/WS 401) — `TestVNCViewTokenGuardLive`.
- **Sidecar lifecycle**: restart-on-crash + idle-shutdown (`Supervisor`, unit
  tests) and a clean process-group kill (no orphan Xvfb/Firefox).

## Bot-detection comparison (B0 measurement)

Loaded each page on both backends from this host's Docker IP (login page only, no
submission). A **measurement**, not a pass/fail gate. Screenshots in `e2e/.b0/`.
Caveat: a datacenter IP fails IP-reputation checks regardless of engine, so this
measures fingerprint/headless evasion, not IP reputation.

| Date | Site | chromium | camoufox | Notes |
|------|------|----------|----------|-------|
| 2026-11-21 | bot.sannysoft.com | UA Chrome/114 (macOS); WebDriver checks pass | UA **Firefox/158 (Windows)**; WebDriver checks pass | camoufox presents a consistent non-host OS; "Chrome: missing" is expected for Firefox, not a failure |
| 2026-11-21 | abrahamjuliot.github.io/creepjs | distinct fingerprint id | **different** fingerprint id (Windows Firefox) | both produce a stable FP id; the two engines are clearly distinct |
| 2026-11-21 | arh.antoinevastel.com/bots/areyouheadless | 502 (site down) | 502 (site down) | site returned nginx 502 for both; no verdict |

Takeaway: camoufox presents a coherent Windows-Firefox identity distinct from the
host and from the chromium backend, and passes the standard WebDriver/headless
tells. From a datacenter IP neither engine evades IP-reputation; camoufox's value
is fingerprint/TLS/JS-surface evasion, best paired with a residential proxy.
