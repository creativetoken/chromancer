# ADR 0017 — Agent-only driver: the LLM agent decides every step

**Status:** Accepted · **Date:** 2026-11-21 · **Supersedes:** [ADR 0004](0004-agent-loop.md)

## Context

ADR 0004 chose a *deterministic-first* loop: a hand-written skill layer (entry
finder, form/multistep handlers, verify-step detector, CAPTCHA hooks) drove the
common path, and an LLM agent took over only on a dead end. That edge — "logins
are ~90% pattern-matchable" — held for the lab, but on real SPAs the
deterministic entry finder and flow handlers are brittle: they fail *before* the
agent ever runs (e.g. `entrypoint_not_found` on a JS-rendered login form), so the
agent could not rescue the very cases it exists for. Maintaining two drivers (a
large deterministic orchestration plus the agent) also doubled the surface for
every new flow.

The agent-takeover infrastructure built for recovery — the `explore.Explorer`
loop, the account-safety deny-list, secret-placeholder substitution, the
untrusted-DATA fence, and the verification/CAPTCHA/success/human tools — is
already a complete driver. It was only ever gated behind the deterministic path.

## Decision

**The LLM agent is the sole driver of authentication and crawl.** The agent
decides every step — find and fill the login/signup form, dismiss banners, clear
interstitials, enter verification codes or open magic links, solve CAPTCHAs — and
success is confirmed **only** by `check_logged_in` (deterministic cookie/XHR
detection). There is no deterministic entry finder, flow handler, resolve loop,
or recipe replay on the hot path.

- **One loop for auth and crawl.** `Authenticator` in `AuthDriverAgent` mode
  navigates to the entry URL and hands the whole flow to `explore.Explorer`,
  wired with the auth tools (`fill_login_form`, `fill_signup_form`,
  `dismiss_banner`, `enter_verification`, `resend_verification`, `solve_captcha`,
  `check_logged_in`, `request_human`). Crawl uses the same Explorer with the base
  action set. System prompts: `prompts/auth_system.md`, `prompts/crawl_system.md`.
- **The deterministic code becomes tools, not drivers.** Form filling, success
  detection, verification and CAPTCHA solving are kept and invoked *by the agent*
  through tool callbacks; the orchestration that sequenced them is removed.
- **Escalation:** agent → human → `failed{reason}`. A bot-wall goes straight to a
  human (the agent is never pointed into one); wrong credentials fail without
  retries; budgets and loop-detection bound the run; the agent never guesses URLs.
- **A real LLM provider is required.** With no model configured, agent-primary
  auth fails fast with a clear message (there is no deterministic fallback to
  degrade to). Cheap-step vs plan-model routing stays configurable via the
  `ModelRouter`. Tests drive the loop with a deterministic, test-only "lab
  policy" model (`internal/lab`, guarded by `testing.Testing()`), never a real
  key, and assert no password/code/seed reaches the model input.

## Rationale

- One driver, one place to improve. Every flow the agent learns to handle works
  for auth and crawl; there is no second code path to keep in sync.
- The cases that matter (unknown SPAs, novel interstitials, shifting verification
  screens) are exactly where hand-written handlers were weakest and the agent is
  strongest — and now the agent reaches them from step one.
- The safety and privacy guarantees are unchanged: they live in the Executor and
  the tool callbacks, which the agent already went through as the recovery
  driver, so "no secret reaches the model" and the account-safety deny-list hold
  identically.

## Consequences

- Every auth/crawl run spends model tokens; there is no zero-LLM happy path. This
  is the accepted cost of removing the brittle deterministic orchestration, and
  it is bounded by budgets and cheap/plan routing.
- Reliability now rests on prompt quality and the tool set rather than on skill
  coverage. The lab-policy e2e suite is the regression bar: the full lab auth
  surface must pass agent-only on both browser backends (chromium and camoufox).
- The deterministic drivers (entry finder, flow handlers, resolve loop, BFS/
  hybrid crawl) are deleted once the agent-only path passes that bar on both
  backends; `--driver` is accepted-but-ignored for one release with a deprecation
  warning.

## Rejected alternatives

- **Keep deterministic-first (ADR 0004).** Rejected: the brittle entry finder /
  handlers fail before the agent runs, and two drivers double the maintenance.
- **Agent-primary with a deterministic fallback.** Rejected for the hot path: a
  fallback re-introduces the second driver and its failure modes; a human handoff
  is the one escalation, which is auditable and safe.
