# ADR 0012 — No scope enforcement (removed)

**Status:** Accepted, re-scoped 2026-11-15 · **Date:** 2026-11-15

> **Re-scope:** earlier versions had a host-allowlist `ScopeGuard` (and before that, a recon-era
> rules engine). **v1 removes scope enforcement entirely.** The decision is recorded here rather
> than deleting the ADR, so the history is explicit.

## Context
v1's job is to authenticate and hand the session back. Authentication legitimately crosses hosts
— OAuth/SSO IdPs, magic-link domains, CAPTCHA vendors — and the operator runs this against an app
they're authorised to use. A host allowlist added friction (every IdP/magic-link host had to be
pre-listed or login broke) for no benefit at this scope: the agent isn't crawling or acting
broadly; it's driving one auth flow to completion.

## Decision
**There is no `ScopeGuard`, no host allowlist, and no `PolicyEngine`.** The agent navigates
wherever the auth flow leads. Navigation and all actions are still **audited** (append-only,
redacted), so there is a complete record of where it went — just no gate.

Phase 2 (authenticated crawl, design only for now) **does not reintroduce scope** either; the
single guardrail it adds is an **account-safety deny-list** (logout, delete account, change creds,
revoke sessions) so a long crawl can't log itself out — enforced at an action-execution boundary,
not as a scope allowlist (ADR 0013).

## Rationale
- The only thing scope protected against here was wandering, and v1 doesn't wander — it follows a
  single auth flow. The audit log already records every navigation.
- Pre-listing IdP/magic-link/CAPTCHA hosts was pure friction that broke logins when missed.
- Removing it deletes a whole interface (`ScopeGuard`), config surface (host lists), and a class of
  "login blocked by scope" failures.

## Consequences
- No `ScopeGuard` interface, no `allow_hosts`/`auth_hosts` config, no per-request host check.
- The browser follows redirects freely; the operator relies on using an app they're authorised to
  use and on the audit log for the record of where the agent went.
- If a future need arises to constrain navigation, it returns as a new ADR — not assumed here.

## Rejected alternatives
- **Host allowlist (prior v):** friction (IdP/magic-link pre-listing) for no benefit at this scope.
- **Navigation deny-list:** nothing concrete to deny in an auth flow; the account-safety deny-list
  (Phase 2) covers the one real hazard (logging ourselves out), and that's about *actions*, not hosts.
