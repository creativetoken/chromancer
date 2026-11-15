# ADR 0002 — Browser Driver: go-rod over CDP, behind our own interface

**Status:** Accepted · **Date:** 2026-11-15

## Context
We need to drive a real Chromium browser from Go with: accessibility-tree extraction,
full network interception, multiple isolated contexts per identity, persistent profiles,
iframe/shadow-DOM/popup handling, file upload/download, and robust SPA waiting. Options:
`chromedp`, `go-rod`, `playwright-go`, or raw CDP.

## Decision
**Use `go-rod` as the initial driver, wrapped behind our own `browser.Browser`/`Page`
interfaces (ADR INTERFACES §browser).** Chromium only, via CDP. Headful by default for
realistic behaviour and operator visibility; headless as a config flag. One CDP connection
exposed where we need raw access (network domains, accessibility domain) that the wrapper
doesn't yet cover.

## Rationale
- **go-rod** gives an ergonomic, actively-maintained, Chrome-only CDP API with good
  defaults (auto-wait helpers, easy `Target`/context handling) while still exposing the raw
  CDP client when we need the Accessibility and Network domains directly — which we do, for
  a11y perception and passive traffic capture.
- **chromedp** is equally capable and lower-level; comparable performance. It's a fine
  fallback and the abstraction lets us swap to it, but its action-builder API is more
  verbose for the interactive, stateful control we're doing.
- **playwright-go** buys cross-browser (Firefox/WebKit) at the cost of a heavier runtime
  (browser binaries + protocol layer) and a community port that lags upstream. We don't
  need non-Chromium for recon, and the extra layer complicates raw CDP access for capture.
- Wrapping behind our interface means the engine choice is reversible and testable (we can
  fake `Page` in unit tests without a browser).

## Consequences
- Chromium-only at launch. Acceptable: targets render in Chromium; cross-browser is not a
  recon requirement.
- We own the a11y extraction (via CDP `Accessibility.getFullAXTree`, pruned to interactive
  + landmark nodes) and the network tap (CDP `Network`/`Fetch` events) rather than relying
  on a high-level helper — more control, exactly what perception and capture need.
- Persistent profiles + fingerprint config (UA, viewport, timezone, locale) are set per
  `Context` (see ADR 0009 on fingerprinting posture).

## Rejected alternatives
- **Raw CDP, no library:** maximal control, but we'd reimplement target/session plumbing
  go-rod already does well. Not worth it.
- **playwright-go:** cross-browser we don't need + heavier; keep as a possible second impl
  behind the interface if a target ever demands WebKit.
- **Remote/managed browser service:** conflicts with local-only / data-redaction posture.
