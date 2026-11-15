# ADR 0015 — Interface: job-based CLI + MCP (start_auth / get_job / resolve_challenge / export_session)

**Status:** Accepted, re-scoped 2026-11-15 · **Date:** 2026-11-15

## Context
v1 authenticates and hands the session back. Authentication can take **minutes** — verification
waits on the operator, CAPTCHA may need a human, flows retry. A synchronous call would tie up a
worker and time out. The agent must be usable both directly (CLI) and by an external harness (MCP),
with the same behaviour.

## Decision
Expose **one job-based control surface** over a single core (`JobManager`), wrapped by a **CLI** and
an **MCP server**, with a **per-account lock** (one operation per account at a time):

- **`start_auth(target, account, mode=login|signup, login_url?, signup_url?)` → `{job_id}`** —
  launches an auth job, returns immediately. The optional URLs are entrypoint hints (§6.0).
- **`get_job(job_id)` →** one of:
  - `running`
  - `needs_verification{challenge_id, channel, hint, expires_at}`
  - `needs_human{challenge_id, view_url, reason}`
  - `done{bundle_ref, recording_ref, trace}` — **references** (file paths for the CLI, MCP resource
    URIs for MCP), never inline payloads: artefacts are large and contain secrets.
  - `failed{reason}` — named reasons: `sso_only`, `budget_exhausted`, `cancelled`,
    `handoff_timeout`, `verification_timeout`, `flow_failed`.
- **`resolve_challenge(challenge_id, value)`** — **one call for both pending states**: a code, link
  or approval answers `needs_verification`; `done` answers `needs_human`.
- **`cancel_job(job_id)`**
- **`export_session(account)`** — returns a **reference** to the latest `SessionBundle` for an
  account for reuse elsewhere.

The **CLI** answers verification/human handoff with an **interactive prompt**; the **MCP** surface is
async (poll `get_job`, answer pending challenges). Budgets, audit and the per-account lock apply
identically to both. The returned bundle/recording are sensitive (ADR 0016) — returned only to the
caller, redacted in logs.

## Rationale
- A minutes-long auth with human-in-the-loop *must* be a job, not a blocking call — otherwise the
  harness can't do anything else and calls time out.
- One `resolve_challenge` entry point for both pending kinds (verification and human handoff)
  keeps the surface tiny; the harness just answers whatever `get_job` is waiting on.
- Artefacts by reference keep job responses small and keep secrets out of transport logs; MCP
  resource URIs let the harness fetch exactly what it needs.
- Named failure reasons make every dead end an exit the harness can branch on (invariant 9).
- Thin CLI + MCP over one `JobManager` guarantees identical behaviour and one audit story.
- The per-account lock prevents two jobs fighting over one account's browser context.

## Consequences
- `internal/server` implements the MCP tools + `JobManager`; `cmd/agentbrowser` implements the CLI.
- No MCP call blocks on a human; the harness polls `get_job`. The CLI keeps the simpler blocking
  prompt for interactive use.
- `start_auth` takes an explicit `mode` (login/signup) — the caller decides; signup still switches to
  login internally on an "account exists" error.
- `export_session` output carries tokens — redacted in logs, returned only to the authenticated caller.

## Rejected alternatives
- **Synchronous `ensure_session`/`browse` calls:** can't survive minutes-long human waits; worker
  starvation and timeouts. Rejected for the job model.
- **Separate verbs for verification vs human handoff answers:** unnecessary; one `resolve_challenge`
  with a typed `Answer` covers both.
- **Inline bundle/recording in `done`:** bloats responses and leaks secrets into transport logs;
  references are returned instead.
- **CLI only / MCP only:** humans want a CLI; the harness wants typed MCP tools; both are cheap over
  one core.
