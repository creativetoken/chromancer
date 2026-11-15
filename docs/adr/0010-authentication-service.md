# ADR 0010 — Authentication as a Service (`Authenticator`), single owner incl. CAPTCHA & verification

**Status:** Accepted, re-scoped 2026-11-15 · **Date:** 2026-11-15

> **Re-scope (v1):** credentials are **operator-provided, never generated**. Verification is via
> a pluggable **`VerificationProvider`** (ADR 0014). The Authenticator's single entry point is now
> **`Run(target, account, mode)`**, invoked by the JobManager, and on success it **produces the
> session handback** (SessionBundle + AuthRecording; ADR 0016) — that handback *is* the v1 product.
> The **deny-list is removed from v1** (no browsing happens here); it returns in Phase 2 (ADR 0013).
> CAPTCHA escalates to **human handoff** when fully blocked (ADR 0011).

## Context
Authentication is a top priority. CAPTCHA was once split between the skill layer and the loop,
and auth steps were loose skills with no home for session lifecycle. Real apps present many flow
variants (multi-step, modal/iframe, OAuth/SSO, magic-link, OTP, TOTP MFA) that need shared state
and one decision-maker.

## Decision
Make authentication a **service** — the `Authenticator` — invoked via one entry point,
`Run(target, account, mode)`. It is the **single owner** of login, signup, verification (via
`VerificationProvider`), CAPTCHA + human handoff (ADR 0011), success detection, session restore,
and producing the handback.

**Credentials are operator-provided.** The store holds the email/username, password, optional
TOTP seed and optional phone the operator supplies (config or tool); the agent never invents an
identity. For signup forms that ask for *incidental* fields (name, company, address, DOB), values
come from an **optional operator profile**, else the agent **generates sensible values** (config
flag). Only incidental fields are ever generated — never credentials.

**`Run` resolves the path:**
1. **Restore** a healthy saved session → done, no login.
2. **Entrypoint discovery** (ARCHITECTURE §6.0): operator `login_url`/`signup_url` hints → the
   per-target cache → deterministic discovery from the root (sign-in/register link & a11y names,
   common paths, header controls opening a modal, accepted only on a login/signup form signature)
   → cheap LLM fallback. Cached per target; reported in the trace.
3. **mode=login** → run the login flow with the provided creds.
4. **mode=signup** → the **signup sequence**: signup with the provided email+password (+profile)
   → **verify** (link/code) → **log in if the app did not authenticate us after verification**. If
   the app says the account already exists → **switch to login**.
5. **SSO-only app** → `failed{reason: sso_only}` fast — OAuth/SSO is not a v1 flow (native login
   is used when it exists alongside SSO buttons).
On success it runs the post-login capture window + replayability check and writes the
**SessionBundle + AuthRecording** to the workspace (ADR 0016), returning references.

**Flow handlers own the form submit and form-error recovery.** A submit error is classified and
recovered in the handler: field/validation error → fix & resubmit; "account exists" on signup →
switch to login; give up after N. Owning submit is also why the handler owns the CAPTCHA submit
+ `Resolver.Outcome` (ADR 0011).

**Flow variants are first-class**, each with a handler, selected deterministically where possible
(cheap LLM classification only when ambiguous): single-page form · multi-step (email → password)
· modal/iframe · magic-link · email/SMS OTP · **TOTP MFA (built-in generator when the operator
supplied a seed, else VerificationProvider)**. **OAuth/SSO is not a v1 flow**: native login is used
when it exists alongside SSO; SSO-only fails fast with `sso_only`.

**Verification is pluggable (ADR 0014):** the agent detects the channel (email/SMS/TOTP/push) + a
hint, then calls `VerificationProvider.GetCode(Request{channel, account, hint, since, deadline})`
→ `Code | Link | Approved`, with `Resend`. Over MCP it's async (job → `needs_verification` →
`resolve_challenge`).

**CAPTCHA + human handoff (ADR 0011):** the flow handler drives the `Resolver` chain and, at **any
dead end** — chain exhausted, block page, challenge loop, **unrecognised flow, stuck after N
steps** — escalates to `HumanHandoff` (headful, or a localhost-bound / one-time-token remote view +
notification; over MCP → `needs_human{challenge_id, view_url, reason}`, resolved by
`resolve_challenge(id, done)`).

**Auth success detection is deterministic** (ARCHITECTURE §6.3): a new auth cookie/bearer-JWT
appeared, `/me` returns 200, an authenticated-only element is present, or the login form is gone.
LLM judgment is a tiebreaker only.

**Session restore:** a saved healthy session is imported and verified at job start; if good, no
login flow runs. JWT `exp` is parsed and the storage-location map recorded for the bundle.

**Multiple accounts**, each in its **own persistent browser context** (isolated, and a
returning-user profile reduces CAPTCHAs).

> **No deny-list in v1.** v1 only authenticates; it doesn't browse, so there's nothing to deny. The
> account-safety deny-list returns in **Phase 2** (ADR 0013), enforced at an action-execution
> boundary so a crawl can't log itself out.

## Rationale
- One owner removes the split-brain CAPTCHA problem and gives session lifecycle a home.
- Flow handlers keep each variant's quirks isolated and testable against the benchmark lab.
- Deterministic success detection is cheaper, more reliable, and auditable than a vision call.
- Operator-provided creds + pluggable verification match the real workflow: the operator owns the
  accounts and the code/link delivery; the agent just drives the browser.

## Consequences
- The store holds operator-supplied creds (incl. optional TOTP seed) rather than generating them;
  no mail-polling infrastructure.
- `Run` produces the handback (ADR 0016); the JobManager surfaces `needs_verification`/`needs_human`
  and the final `done{bundle, recording}`.
- Verification/human-handoff latency is bounded by `ctx`/`Deadline` and the async job model, so a
  minutes-long wait never blocks a worker.

## Rejected alternatives
- **Generating identities / capturing TOTP seeds (old design):** out of the new scope — the
  operator provides accounts. Removed.
- **MailProvider the agent polls:** replaced by the ask/answer `VerificationProvider`, which also
  covers SMS and push without the agent needing inbox access.
- **LLM-driven "is it logged in?" every time:** costly and flaky; deterministic signals win.
- **Single shared context for all accounts:** sessions bleed. Rejected.
