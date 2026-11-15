# ADR 0008 — CAPTCHA Strategy: detect & classify in-core, solve via a pluggable chain

**Status:** Accepted, amended 2026-11-15 · **Date:** 2026-11-15

> **Amendment:** the *policy stance* below stands (core ships no challenge-defeating logic;
> human-in-the-loop is the default; automated solving is operator-supplied and audited). The
> *mechanics* — the plugin contract, `Detector`/`Injector`/`Resolver`/`Solver` split,
> `Result` types, in-process vs gRPC solvers, Budget cost accounting, and the conformance
> suite — are specified in **ADR 0011**, which this ADR defers to. Note: there is no policy
> engine in the re-scoped design (ADR 0012); the solver **chain is explicit per-target config**
> and every solve is audited, so "gated" below means "chosen by the operator's configured chain
> and audited," not gated by a runtime policy engine.

## Context
Signup/login flows hit CAPTCHAs: reCAPTCHA v2/v3, hCaptcha, Cloudflare Turnstile,
Arkose/FunCaptcha, and custom challenges (RESEARCH §5). The agent must *handle* these to
proceed into an app the operator is authorised to use, but baking challenge-defeating logic
into the core is the wrong default: whether automated solving is acceptable depends on the
app's terms and the operator's authorisation, so it must be an explicit, operator-owned choice
rather than a built-in capability.

## Decision
Split the problem into **detection (ours) and resolution (a pluggable, operator-configured chain)**:

- **Detection & classification — built in, deterministic.** A `Detector` identifies the
  vendor and challenge type from script sources, iframe origins, sitekeys and DOM
  signatures. No LLM, no network, no bypass logic. This is the part we own and ship.
- **Resolution — a pluggable `Solver` chain run by a `Resolver`** (ADR 0011). The per-target
  chain is explicit config and every solve attempt is written to the audit log. Strategies, in
  default order:
  1. **Human-in-the-loop (default, shipped):** pause the run, surface the live challenge to
     the operator, resume on their signal. No automation of the challenge itself.
  2. **External solver plugin (operator-configured, off by default):** delegate to a
     licensed third-party service the *operator* supplies and is responsible for. The core
     ships no such provider; it only defines the interface.
  3. **Test-mode bypass (benchmark lab only):** inject known test tokens for the operator's
     *own* mock apps / known test sitekeys, for reproducible evaluation.
- **Escalation to human handoff when fully blocked.** If the chain is exhausted or the agent is
  blocked (repeated challenges, block/denied page, challenge loop), it escalates to `HumanHandoff`
  — live browser access (headful or remote DevTools/screencast) + notification; over MCP the job
  surfaces `needs_human{view_url}` (ADR 0011/0015). This is the backstop that keeps a hard block
  from silently failing the run.
- **The core ships no challenge-defeating logic.** Automated solving is entirely an external,
  operator-supplied, audited plugin.

## Rationale
- Keeps the default safe: a human in the loop and no circumvention; anything beyond that is an
  explicit, logged, operator-owned choice tied to their authorisation and the app's terms.
- Detection is genuinely useful and fully deterministic — it lets the agent *know* it's
  blocked and route correctly — without crossing into circumvention.
- The audit log means "did we solve a CAPTCHA, when, how, via which solver" is always answerable.

## Consequences
- Many apps will *pause for the operator* at signup/login by default. That's the correct, safe
  behaviour; operators who have cleared automated solving configure a plugin and accept
  responsibility.
- The benchmark lab uses test-mode challenges so auth metrics are reproducible without any real
  circumvention.
- CAPTCHA handling never silently proceeds: the `Resolver` chain is explicit per target and
  every solve is audited.

## Rejected alternatives
- **Build automated solvers into the core:** the wrong default — acceptability depends on the
  operator's authorisation and the app's terms. Rejected in favour of operator-supplied plugins.
- **Ignore CAPTCHAs:** the agent would get stuck with no signal; detection is necessary even
  when resolution is human-only.
- **Always require a human (no plugin seam):** too rigid for operators who have cleared automated
  solving; the pluggable chain handles both cases while keeping the safe default.
