# ADR 0011 — CAPTCHA as a Pluggable Solver Service (Detector/Injector core, Solver plugins)

**Status:** Accepted · **Date:** 2026-11-15 · **Refines:** ADR 0008 (policy stance unchanged)

## Context
The first CAPTCHA design hardcoded three `alt` branches (human / external / test-mode) in the
login sequence. That isn't extensible: adding a vendor or a solving approach means editing the
core flow, solvers could reach into the browser, and there was no way to rank solvers or cap
their cost. We need a plugin contract where the **core owns everything that touches the
browser** and **plugins own only solving**.

## Decision

**Core owns, per vendor:**
- **`Detector`** — recognises a challenge from visible elements *and* invisible/score-based
  signals (reCAPTCHA v3, Turnstile) via **network + script signals and failed-submit
  responses**, not just DOM presence.
- **`Injector`** — applies a solved result to the page (inject token, click set-of-marks
  regions, or no-op after a live human handoff). **Only the core injects.**

**Plugins own solving only and never touch the browser.** `Solver` interface:
- `Describe() Capabilities` — declares `vendors`, `kinds`, `NeedsImage`, `NeedsProxy`
  (IP-bound tokens), `EgressExternal`, and estimated cost/latency. **Called once at plugin
  load**; the Resolver caches it and **pre-filters** its chain by capability, so no
  `Describe()` happens on the hot path.
- `Solve(ctx, Challenge) Result` — where **`Result` = `Token` | `UIActions`** (clicks on
  set-of-marks regions the core will perform) **| `Handoff`** (a human solved it live).
- `Report(ctx, solveID, accepted)` — the acceptance signal (forwarded by `Resolver.Outcome`)
  so solvers are **ranked per vendor** by real acceptance.

**Built-in, in-process solvers:** `testmode` (known test sitekeys for the lab), `human`
(pause + notify the operator), `vision` (set-of-marks reasoning via the Model Router).
*Session reuse is **not** a solver* — "try an existing valid session first" is the
Authenticator's `Run` (ADR 0010), which restores a session before any CAPTCHA is ever reached, so
the old `avoid` solver is removed. **External solvers run out-of-process** via
`hashicorp/go-plugin` over **gRPC**; the same proto serves a remote solver service. The core
ships **no** external solver — only the contract.

**The flow handler owns the submit and the retry loop.** The `Resolver` applies the next
eligible solver's `Result` to the page (via the core `Injector`) and returns `{Result,
solveID}`; it does **not** submit. The flow handler submits the login/signup form, decides
whether the solve was accepted (login advanced vs challenge re-shown), and calls
**`Resolver.Outcome(solveID, accepted)`**. On rejection, the flow handler **re-runs the
Detector to get a fresh challenge before retrying**, because CAPTCHA tokens are single-use;
the next `Resolve` advances to the next eligible solver. **Max solves per run and max solver
cost per run** are counted in **Budget** (alongside LLM tokens). Every step is
redacted-then-audited.

**Human handoff for any dead end (escalation, not a solver).** When the eligible chain is
**exhausted** (`Resolver.Exhausted()`) or the agent is **fully blocked** — repeated challenges, a
block/denied page, a challenge loop — *and equally* when the auth flow is **unrecognised** or the
agent is **stuck after N steps**, the flow handler escalates to a separate **`HumanHandoff`** (not a
Solver; it may act on the browser) with a typed `BlockReason`. It gives a human live access to the
real browser: **headful** on the operator's machine, or a **remote DevTools / screencast** view on
a remote host. It fires a **notification** and blocks on the operator's signal; over MCP the
JobManager surfaces **`needs_human{challenge_id, view_url, reason}`**, resolved by
`resolve_challenge(id, done)`; an unanswered handoff fails the job with `handoff_timeout`.
**Protecting the remote view:** it is full control of an authenticated browser, so `view_url` is
**localhost-bound by default** and, when exposed beyond the host, carries a **one-time token** that
expires when the challenge is resolved or the job ends. The trace records why handoff fired.

**Conformance:** ship a **mock solver plugin** and a **conformance test suite** every plugin
(in-process or gRPC) must pass — covering the `Describe`/`Solve`/`Report` contract, each
`Result` type, timeout/cancellation, and that it never attempts browser access.

## Rationale
- Core-owns-browser / plugins-only-solve is the clean security and extensibility boundary: a
  third-party solver (possibly external egress) can't drive the browser or see more than the
  `Challenge` it's handed.
- The typed `Result` (Token | UIActions | Handoff) covers every real solving modality without
  the core knowing *how* it was solved.
- `Describe`/`Report` make the chain self-tuning: the Resolver learns which solver actually
  works per vendor and orders accordingly, within a hard cost cap.
- go-plugin/gRPC gives process isolation and a proto that doubles as a remote-service API,
  so "local plugin" and "hosted solver" are the same contract.

## Consequences
- Budget gains a **solver-cost and solve-count** dimension (DATA_MODEL budget ledger; invariant 5).
- Invisible-CAPTCHA detection requires wiring network/script signals + failed-submit responses
  into the Detector, not just the a11y snapshot.
- Policy stance from ADR 0008 is unchanged: default path is `human`; the `testmode` built-in and
  any external plugin are operator choices set in the per-target chain config and audited (there
  is no runtime policy engine — ADR 0012).

## Rejected alternatives
- **Hardcoded alt branches (original):** not extensible, no ranking, no cost cap, solvers
  entangled with the flow. Rejected.
- **Plugins drive the browser themselves:** breaks the security boundary and couples solvers
  to our driver. Rejected — plugins return a `Result`, the core applies it.
- **In-process only (no gRPC):** can't isolate untrusted external solvers or support a remote
  service. Rejected; go-plugin covers both in one contract.
