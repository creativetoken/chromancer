# ROADMAP

> v1 goal: **authenticate and hand the session back.** Thin vertical slice first. Each
> milestone: scope, acceptance, a lab demo, rough effort (engineer-weeks, one experienced Go
> engineer). Budgets, audit (redaction), and the per-account lock are built into M0 and every
> path from the first commit. **No scope enforcement anywhere.**

Legend: **S** ≤1w · **M** 1–2w · **L** 2–4w · **XL** 4w+.

---

## M0 — Skeleton + foundation
**Scope:** project layout; config + per-account workspace; SQLite store + migrations; append-only
hash-chained audit log with **redaction before write**; Budget/kill-switch (tokens + solver cost);
credential store (operator creds + profile, encrypted); one `LLMProvider` (Anthropic *or* an
OpenAI-compatible local endpoint) with the structured-output ladder (tiers 1–3); go-rod browser
wrapper with a **persistent profile per account**, navigate/snapshot(a11y)/act/wait, optional
upstream proxy. No ScopeGuard.
**Acceptance:** navigate, produce an a11y snapshot, perform a typed action; every step is
budget-charged and redact-then-audited with a verifiable hash chain; a saved session can be
imported into a context.
**Effort:** L.

## M1 — Login with provided creds → SessionBundle + AuthRecording (CLI) ⭐
**Scope:** `Authenticator` v1 (`Run`, mode=login) with **entrypoint discovery** (§6.0: `login_url`
hint → per-target cache → link/path/modal probe → LLM fallback), the single-page-form
`FlowHandler`, deterministic **success detection** (§6.3), and **session restore** (healthy saved
session → no login); `AuthRecorder` with the **post-login capture window** (load landing page once,
then N requests / T seconds; bodies, secrets flagged); `SessionBundle` builder (cookies w/ flags +
local/session storage + **auth headers observed on post-login XHR** + UA + proxy + JWT exp);
**replayability check** (plain Go HTTP replay of one authed XHR → `replayable` + reason);
**artefacts written to the workspace and returned by path**; CLI `auth --mode login`.
**Acceptance:** given operator creds, the agent reaches the login form **without a hint** on one
local app and **with a hint** on another, logs in ≥90% of attempts (5 runs), detects success
deterministically, and returns a **SessionBundle that re-imports into a fresh context and is
authenticated without a new login** (incl. a bearer-only app working via the captured auth
headers); the replayability check reports `replayable: true` on a cookie/bearer app and a
**named reason** on a scripted request-signing mock; the **AuthRecording** contains the login
flow + the post-login window with secrets flagged; restore path logs in zero times on a second run;
artefacts are returned as paths, never inline.
**Demo:** `agentbrowser auth --target juiceshop --account alice --mode login` → prints bundle +
recording; the bundle loads elsewhere and lands logged-in.
**Effort:** L. *First end-to-end proof and the go/no-go gate.*

## M2 — MCP job interface + export_session
**Scope:** `internal/server` + `JobManager`: **`start_auth(target, account, mode, login_url?,
signup_url?)` → `{job_id}`**, **`get_job`** (`running | done{bundle_ref, recording_ref, trace} |
failed{reason}` with named reasons `sso_only | budget_exhausted | cancelled | handoff_timeout`),
**`cancel_job`**, **`export_session(account)`** → a bundle reference; artefacts exposed as **MCP
resource URIs**; **per-account lock**; thin adapters over the core so budgets/audit/lock apply
identically to CLI and MCP; MCP tool schemas in the common subset.
**Acceptance:** a harness drives `start_auth(mode=login)` → polls `get_job` → `done` with
`bundle_ref`/`recording_ref` it can read as MCP resources (never inline payloads); a cancelled job
reports `failed{cancelled}` and releases the lock; `export_session` re-imports and is
authenticated; concurrent `start_auth` for different accounts run isolated while a second op on the
*same* account is rejected by the lock; CLI and MCP produce identical audit trails.
**Demo:** harness: `start_auth` → `get_job` (done) → reads `bundle_ref` resource →
`export_session`; bundle loads elsewhere.
**Effort:** M.

## M3 — Signup + verification (CLI prompt, MCP pending/answer, TOTP)
**Scope:** `Authenticator` mode=signup running the **signup sequence** — signup with an
operator-provided **email + password** and **optional profile** (else generated incidental fields)
→ **verify** (link/code) → **log in if the app did not authenticate us after verification**;
**account-exists → switch to login**; **SSO-only → `failed{sso_only}`** fast; `signup_url` hint +
signup entrypoint discovery; the full flow-variant set (single-page, multi-step, modal/iframe,
magic-link, email/SMS OTP, TOTP); **`VerificationProvider`** with **built-in TOTP** + **CLI
prompt** impls, **`since`/`deadline`**, **resend**, result **`Code | Link | Approved`**; channel
detection + hint extraction; over MCP the **async pending/answer** pattern (`needs_verification` →
`resolve_challenge`); form-error recovery.
**Acceptance:** against ≥3 lab apps (incl. an email-OTP, a TOTP, and a "verify-then-login" flow),
the agent signs up + verifies + ends authenticated ≥85% of attempts; incidental fields come from a
profile and (with none) are generated; CLI prompt handles email/SMS OTP incl. a **resend**;
built-in TOTP handles authenticator prompts; over MCP a job surfaces `needs_verification` and
resumes on `resolve_challenge`; an account-exists signup switches to login; an SSO-only mock fails
fast with `sso_only` rather than looping.
**Demo:** `start_auth(mode=signup)` → `needs_verification{email}` → harness answers emailed OTP →
`done{bundle}`.
**Effort:** L.

## M4 — CAPTCHA chain (testmode + external gRPC) + human handoff on block
**Scope:** core `Detector` per vendor (reCAPTCHA v2/v3, hCaptcha, Turnstile, Arkose, custom) incl.
invisible/score; core `Injector`; `Resolver` with a `Describe()`-prefiltered chain, Budget caps,
flow-handler-owned submit + `Outcome`, **fresh-challenge re-detection per retry**, and `Exhausted`;
in-process **`testmode`** + **`vision`** solvers; **go-plugin/gRPC** host + `.proto` + a **mock
external solver** + **conformance suite**; **`HumanHandoff` for any dead end** (CAPTCHA exhausted,
block page, challenge loop, unknown flow, stuck after N steps) — headful or **remote view that is
localhost-bound or one-time-token protected** + notification → over MCP
`needs_human{challenge_id, view_url, reason}` resolved by `resolve_challenge(id, done)`;
`handoff_timeout` on expiry.
**Acceptance:** classifies all five vendor types incl. invisible ones ≥95% in the lab; the chain
applies each `Result` type via the Injector, caps cost/solves, re-detects a fresh challenge on
rejection; the mock gRPC solver loads + passes conformance; scripted **block**, **unknown-flow** and
**stuck** scenarios each escalate to `needs_human` with the correct `reason` and resume on the
operator's signal; an unanswered handoff fails with `handoff_timeout` and releases the lock; the
remote `view_url` is unreachable without its token and the token is dead after resolution; every
step audited.
**Demo:** `start_auth` on a lab app with a test reCAPTCHA → testmode solves; a block scenario →
`needs_human{view_url, reason: block_page}` → operator opens the tokenised view, acts, signals
done → flow completes.
**Effort:** L.

## M5 — Phase 2: authenticated crawl (design in ARCHITECTURE §9)
**Scope:** `Explorer` starting from a SessionBundle — navigate, open items, fill+submit forms,
paginate; **simhash state dedup** (collapse `/item/{id}`); **stop condition** (budget, or no new
states for N iterations); **mid-crawl session loss → re-login → restart sub-goal**; the
**account-safety deny-list** at an action-execution boundary (logout, delete account, change creds,
revoke sessions) so the crawl can't log itself out. Still **no scope allowlist**.
**Acceptance:** on an authenticated lab app the agent visits ≥N distinct real views without looping
(dedup below a set re-visit ratio), keeps the session alive across a forced mid-crawl logout
(auto re-login, sub-goal restarts), and never triggers an account-safety action (a planted logout
control is skipped and recorded).
**Demo:** feed a bundle to the crawler → it uses several features; kill the session mid-run → it
re-logs-in and continues.
**Effort:** XL.

---

## Sequencing notes
- **M1 is the gate.** If provided-cred login + a usable bundle/recording don't land on one app,
  revisit perception/flow-handler design before adding the MCP surface.
- **Spikes before the dependent milestone:** stable a11y handles under SPA re-render (before M3);
  7–8B local model + grammar ladder (during provider work); simhash threshold (before M5).

## Benchmark lab
Juice Shop (SPA, modal login), crAPI (OTP, roles), DVWA (classic forms), plus mocks: multi-step
signup, email-OTP, magic-link, TOTP MFA, login-in-a-modal/iframe, test-mode CAPTCHA, a block/
challenge-loop scenario, session expiry. Metrics (per model): auth success rate, steps/tokens/cost
per success, bundle-reuse success (re-import lands authenticated), human-handoff trigger accuracy,
runtime.
