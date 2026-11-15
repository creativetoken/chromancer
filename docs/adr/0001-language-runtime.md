# ADR 0001 — Language & Runtime: Go core, narrow sidecars only where forced

**Status:** Accepted · **Date:** 2026-11-15

## Context
The brief prefers Go. The ecosystem's strongest LLM-agent and browser libraries are
Python (browser-use) and Node (Stagehand, Playwright MCP). We must decide whether to
accept a polyglot system and, if so, where the boundary sits.

## Decision
**Go is the core and the default for everything.** A non-Go sidecar is allowed **only**
when a capability is both (a) materially better outside Go and (b) cleanly isolatable
behind a process boundary with a typed contract. We expect **zero mandatory sidecars** at
launch; all core loops, perception, model abstraction, guardrails, the authentication
service and the browsing explorer are Go.

Permitted *optional* sidecars, each behind a Go interface and off by default:
- **None required.** Candidates we explicitly reject as sidecars: browser-use/Stagehand
  (we reimplement the small slice we need in Go, not shell to a second agent runtime).
- CAPTCHA solver plugins are the one out-of-process extension point, but they go over a typed
  gRPC contract (ADR 0011), not a language sidecar — a plugin can be written in any language.
- Candidate we *allow* if needed: a thin Python helper for an ML-based CAPTCHA *detector*
  feature — but detection is deterministic, so this is unlikely. If ever added it is an
  operator plugin, not a core dependency.

## Rationale
- Single static binary, trivial deployment, strong concurrency (goroutines map perfectly
  to per-target/per-identity parallelism with per-target rate limiting).
- One language in the core = one audit surface, one build, no cross-runtime serialisation
  tax on the hot path (perception → model → action).
- The "best" Python/Node agent frameworks are autonomy-first and weak on determinism,
  audit and local models — the exact axes we care about — so we're not giving up much by
  not depending on them.

## Consequences
- We write our own CDP driver wrapper, a11y perception, and provider clients. More code,
  but it's the code we most need to control (determinism, budgets, redaction).
- Vision/multimodal is fine: Go HTTP clients post images to provider APIs; no Python needed.
- If a future must-have is Python-only, the process-boundary rule keeps it quarantined and
  the core still ships as one binary with an optional helper.

## Rejected alternatives
- **Python core (browser-use base):** fastest to a demo, but loses the Go brief, the single
  binary, and forces us onto an autonomy-first design we'd fight.
- **Node core (Stagehand base):** best primitives, but same runtime/deployment cost and
  TS, not Go.
- **Polyglot from day one:** premature; adds a serialisation + ops burden before we know we
  need it.
