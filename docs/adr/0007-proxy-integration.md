# ADR 0007 — Optional Upstream Proxy (operator's harness), no traffic capture

**Status:** Accepted, re-scoped 2026-11-15 · **Date:** 2026-11-15

> **Re-scope:** this project no longer discovers endpoints or captures traffic — that's handled
> by a separate harness and agent skills. The earlier "two-layer CDP tap + Burp/Caido exporters"
> design is removed. What remains is a single, simple capability: the operator can point the
> agent's browser at **their own** upstream proxy.

## Context
The agent authenticates and browses a web app like a normal user. The operator may want the
browser's traffic to flow through their own harness/proxy (for their separate testing pipeline),
but the agent itself does no capture, extraction, inventory or export.

## Decision
Provide **one optional setting**: an **upstream proxy URL** on the browser context config. When
set, Chromium launches with `--proxy-server` pointing at the operator's proxy/harness, and the
operator installs their proxy CA into the agent's browser profile for TLS interception. When
unset (default), the agent talks directly to the app. The agent neither inspects nor records the
traffic — it's the operator's harness that does whatever it wants with it.

## Rationale
- Meets the real remaining need ("I'll point it at my own harness") with a single config knob
  and zero new moving parts.
- Keeps this tool focused on its job — get in, stay in, use the app — and leaves traffic
  analysis entirely to the separate harness that owns it.
- No CDP traffic tap, `TrafficSink`, extractors, normaliser, inventory, or exporters to build or
  maintain.

## Consequences
- `browser.ContextConfig` gains an optional `ProxyURL`; everything else is unchanged.
- There is no scope enforcement (ADR 0012), so the proxy sees whatever the auth flow navigates to;
  that's fine — the operator points it at their own harness and all navigation is audited.
- TLS interception is the operator's proxy's responsibility; we just trust its CA in the profile.

## Rejected alternatives
- **Build CDP capture / exporters (old design):** out of the new scope; duplicates the separate
  harness. Removed.
- **Bundle a MITM proxy:** unnecessary; the operator brings their own.
