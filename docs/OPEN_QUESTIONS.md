# OPEN QUESTIONS

> Decisions I need from you. Each has my **recommended default** — if you say nothing, I'll
> proceed with the default and note it. Ordered roughly by how early it bites.

---

### Q1 — First target & benchmark lab
Which app is the M1 "one app" for the thin slice, and which mock apps first?
**Default:** M1 against **OWASP Juice Shop** (SPA, modal login). Build mocks: multi-step signup →
email-OTP → magic-link → TOTP MFA → login-in-a-modal/iframe → OAuth-style redirect → test-mode
CAPTCHA → session-expiry. Add **crAPI** (roles, OTP) for M2–M3 and **DVWA** for a classic-form
baseline.

### Q2 — Primary LLM provider for M0/M1
Frontier API or local model first?
**Default:** **Anthropic** first (clean tool-calling to get the loop working), then add an
**OpenAI-compatible/Ollama** path early so local support is proven, not retrofitted.

### Q3 — How hard is "local-only"?
Must-ship, or a supported-and-measured mode?
**Default:** **supported-and-measured** — report the local 7–8B success rate; if materially worse,
local is best-effort and frontier stays the reliability default.

### Q4 — VerificationProvider: which impls, and default channel handling
Which providers ship first, and what happens when the page wants a channel no provider covers?
**Default:** ship **built-in TOTP** + **CLI prompt** first (M2), **webhook** and **tool/MCP** next
(M5). If no provider `Supports` the detected channel, **pause and surface `needs_verification`**
(MCP) or prompt (CLI) rather than failing — the operator can answer manually. Confirm whether you
want an SMS tool wired early or handled via the generic CLI/webhook path.

### Q5 — CAPTCHA default stance
Confirm: core ships **no** automated solver; human-in-the-loop default; automated solving is an
opt-in, operator-supplied, audited plugin.
**Default:** **yes, as described** (ADR 0008/0011) — right posture even if many flows pause for you.

### Q6 — MCP transport & long-run semantics
How should the harness consume long `browse` runs and pending challenges — polling `get_job`, or a
streaming/notification channel?
**Default:** **polling `get_job`** (simplest, transport-agnostic) for v1, with the pending-challenge
shapes returned inline; add streaming later if your harness prefers it. Confirm your MCP client's
capabilities.

### Q7 — Signup profile generation
When a signup form asks for incidental fields and no operator profile is supplied, generate them?
**Default:** **generate sensible values** (config flag `signup.generate_profile_fields`, default
**on**) — credentials are never generated, only incidental fields. Turn the flag off to force
"operator profile or fail" for apps where fabricated profile data is unwanted.

### Q8 — AuthRecording scope & secret handling
The recording captures the auth flow + first few authed XHRs with **bodies** (password/OTP flagged).
How much, and how are secrets delivered?
**Default:** record the **auth flow + first 3–5 authenticated XHRs**, bodies included, **secrets
flagged but not stripped** (the caller needs to see how auth works), returned only to the caller and
redacted in logs (ADR 0016). Confirm whether you'd rather secrets be **masked** in the recording by
default (safer, but less directly replayable).

### Q9 — Redaction aggressiveness vs. agent effectiveness
Strong redaction protects app data but can blind the model.
**Default:** **structural redaction** — strip obvious PII/token patterns from external-bound payloads
but keep page structure/labels; **local-only mode** as the no-redaction-needed path for sensitive
apps. Tune per target.

### Q10 — export_session handling
`export_session` returns cookies + storage + tokens + auth headers — sensitive. How should it be
delivered/retained?
**Default:** returned **only to the authenticated caller**, **never persisted in the audit log in
clear**, with the proxy/IP-binding note attached. Confirm whether you want it written to a file, or
only returned over the MCP/CLI response.

### Q11 — Concurrency for v1
**Default:** **per-account lock** (one operation per account's context) from the start; parallelism
is across **different** accounts in isolated contexts. Same-account concurrent calls are rejected.

### Q12 — Perception spike acceptance bar
What local-model auth-success rate makes "local-friendly" true enough to build on?
**Default:** **≥70%** on the M1/M2 auth benchmark with a 7–8B local model = "ship as measured mode";
below ~50% = documented experimental, lean on routing (local for classification, strong for planning).

---

## Ambiguities I resolved with an assumed default (carry-on, flag if wrong)

- **A1 — "normal user" browsing depth:** I treat it as *using* main features (open items, search,
  submit own-content forms, paginate, toggle settings) with simhash dedup and a "no new states for
  N" stop — **not** exhaustive crawling. Flag if you want a target number of features/states instead.
- **A2 — no scope enforcement (v1):** the agent navigates wherever the auth flow leads (IdP/magic-
  link/CAPTCHA hosts) with **no allowlist** (ADR 0012); navigation is audited, not gated. I assume
  this is acceptable because the operator runs it against an app they're authorised to use. Flag if
  you want even a minimal "don't leave these domains" guard back.
- **A3 — vision when only non-vision local models are configured:** visual-only steps (e.g. a
  canvas CAPTCHA) **degrade to human handoff** rather than forcing a cloud call, preserving
  local-only mode. Flag if you'd rather auto-fallback to a configured cloud vision model.
- **A4 — "model-agnostic = config not code":** provider + routing are config; per-model prompt
  variants are versioned assets, which I treat as still satisfying the requirement.
