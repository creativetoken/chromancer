# ROADMAP

> Built autonomously from `docs/ARCHITECTURE.md` (the approved spec). A milestone is done only when
> **all** its acceptance criteria pass in automated tests, `docs/PROGRESS.md` is updated, and the
> milestone is tagged (`m1`, `m2`, …). Everything is testable offline against the local lab
> (`cmd/authlab`); real LLM providers are used only when their API-key env var is set.

## Step 0 — Setup
`docs/INTERFACES.md`, this roadmap, Go module scaffold per ARCHITECTURE §10, `Makefile`
(`build test lint lab e2e`), CI config, `README.md`, `docs/PROGRESS.md`, `docs/DECISIONS.md`.

## Step 0.5 — Local test lab (`cmd/authlab`)
One Go binary serving mock web apps on localhost, each on its own path prefix. Each exposes
machine-readable ground truth at `/__lab/expect`; a lab inbox backs the `labinbox`
VerificationProvider.

| Lab app | Exercises |
|---|---|
| `simple` | single-page login form, cookie session, `/api/me` |
| `multistep` | email → password on separate views |
| `modal` | login in a modal opened from a header button; no `/login` route |
| `iframe` | login form inside an iframe |
| `spa-jwt` | SPA (vanilla JS), JWT in localStorage, sent as `Authorization: Bearer` on XHR |
| `hidden-entry` | login only reachable via a non-obvious link text (forces discovery/LLM fallback) |
| `signup-verify` | signup → email code (lab inbox) → must log in after verifying |
| `magic-link` | passwordless, link via lab inbox |
| `otp-sms` | SMS-style OTP via lab inbox |
| `totp` | TOTP MFA with a known seed |
| `form-errors` | rejects first submit (validation error, weak password, "account exists") |
| `captcha-mock` | mock CAPTCHA widget with a sitekey and a verify endpoint (accepts a lab test token) |
| `captcha-block` | always re-challenges / shows a block page (forces human handoff) |
| `sso-only` | only "Sign in with …" buttons (must fail `sso_only`) |
| `bound-session` | rejects requests lacking a JS-computed per-request header (`replayable:false`) |
| `crawl` (M5) | nav menu, paginated list, item pages, a form, logout link, delete-account button, session expiring after X requests |

---

## M1 — Login + session handback (CLI)
- go-rod browser layer with a persistent profile per account; optional upstream proxy setting.
- A11y-tree perceiver; set-of-marks escalation stubbed behind its interface.
- Model abstraction: `LLMProvider` interface, **mock provider**, one real provider
  (OpenAI-compatible, also covers Ollama/vLLM), and the router.
- Entrypoint discovery (§6.0): hints → cache → link/path/modal probe → LLM fallback.
- Login flow handlers: single-page, multi-step, modal, iframe.
- Success detection (§6.3), session restore (§6.4).
- AuthRecorder with post-login capture window, SessionBundle, replayability check (§7.4),
  artefact writer.
- Credential store (encrypted at rest; key from env var), audit log with redaction, budgets.
- CLI: `agentbrowser auth --target --account --mode login [--login-url]`, `export-session`.

**Accept:** e2e passes against `simple`, `multistep`, `modal`, `iframe`, `spa-jwt`, `hidden-entry`,
`bound-session`. Second run restores the session without logging in. Bundle contains the bearer
header for `spa-jwt`. `bound-session` → `replayable:false` with a reason; others →
`replayable:true`. Audit log contains no plaintext passwords or tokens (test greps for them).

## M2 — Job-based MCP server
- JobManager with per-account lock and job states from §8.
- MCP server (stdio transport first; streamable HTTP optional) exposing `start_auth`, `get_job`,
  `resolve_challenge`, `cancel_job`, `export_session`. Artefacts returned as resource URIs.

**Accept:** an MCP client test drives a full login via the tools; concurrent `start_auth` on the
same account is serialised; `cancel_job` cleans up the browser context.

## M3 — Signup + verification
- Signup flow with operator profile / generated incidental fields; "account exists" → login;
  signup → verify → login.
- VerificationProvider implementations: CLI prompt, MCP pending/answer (`needs_verification` →
  `resolve_challenge`), webhook, built-in TOTP, and `labinbox` for tests. `since`, `deadline`,
  `Resend`.
- Form-error recovery.
- SSO-only detection → `failed{reason: sso_only}`.

**Accept:** e2e passes against `signup-verify`, `magic-link`, `otp-sms`, `totp`, `form-errors`,
`sso-only`, both via CLI and via MCP (with the test acting as the harness answering
`needs_verification`).

## M4 — CAPTCHA + human handoff
- Core Detector/Injector (mock vendor first; then reCAPTCHA v2, hCaptcha and Turnstile detectors
  based on DOM/script/network signatures, with unit tests on saved HTML fixtures).
- Resolver: `Describe()` once at load, prefiltered chain, budget, `Outcome()`, fresh challenge per
  retry.
- Solvers: in-process `testmode`, `vision` (via ModelRouter; mock-tested), go-plugin/gRPC host +
  `.proto` + **mock external solver plugin** + conformance test suite.
- HumanHandoff for any dead end: headful mode, remote view bound to localhost or one-time token,
  notification hook, `needs_human` → `resolve_challenge(done)`; handoff timeout →
  `failed{handoff_timeout}`.

**Accept:** `captcha-mock` solved via `testmode` and via the mock gRPC plugin; `captcha-block`
escalates to `needs_human`, and a test that completes the challenge through the remote view and
calls `resolve_challenge` gets `done`; stuck/unknown-flow escalation covered by a test; conformance
suite passes for the mock plugin.

## M5 — Phase 2: authenticated crawl
- Explorer per ARCHITECTURE §9: start from a SessionBundle, navigate, open items, fill and submit
  forms with sensible data, paginate; simhash state dedup collapsing parameterised routes; stop on
  budget or no new states for N iterations.
- Account-safety deny-list at an action-execution boundary (logout, delete account, change
  password/email, revoke sessions/API keys).
- Mid-crawl session loss → re-login → restart in-flight sub-goal.
- `crawl` lab app; CLI `agentbrowser crawl` and MCP `start_crawl` job.

**Accept:** crawl reaches all ground-truth states of the `crawl` app, never clicks logout or
delete-account, survives the forced session expiry, terminates on plateau.

## M6 — Hardening & docs
- Eval harness: run all lab e2e scenarios per configured model, output a results table (success
  rate, steps, tokens, cost, time).
- Real-provider adapters for Anthropic and Gemini (compiled and unit-tested with recorded
  responses; live tests only when keys are present).
- README: install, config reference, CLI and MCP usage, writing a solver plugin, writing a
  VerificationProvider, security notes on artefacts.
- Final pass: remove dead code, `make lint test e2e` green, PROGRESS.md "complete" with deferred items.
