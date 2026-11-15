# RISKS

> Scope: an agent-driven browser that authenticates with operator-provided credentials, handles
> verification/CAPTCHA, and browses an app the operator is authorised to use. Each risk:
> likelihood × impact, and a concrete mitigation already in the design.

---

## Authorisation / side-effects

### R1 — (v1) Side-effects during authentication · likelihood Low · impact Med
v1 only drives an auth flow, so it shouldn't take broad side-effecting actions — but a signup flow
can still, e.g., trigger a "resend"/email. **No scope or deny-list in v1** (ADR 0012).
**Mitigation:** the agent's actions are confined to the auth flow by the Authenticator (it's
following login/signup, not browsing); everything is audited. The autonomous-browsing hazard
(payments, external invites, public posts) and the **account-safety deny-list** belong to **Phase 2**
(ADR 0013), where the crawl acts broadly — not v1.

### R2 — CAPTCHA/anti-automation acceptability · likelihood Med · impact Med
Automated solving may be against an app's terms.
**Mitigation:** core ships no challenge-defeating logic; default is human-in-the-loop; automated
solving is an opt-in, operator-supplied, audited plugin (ADR 0008/0011); hard blocks escalate to
**human handoff**, not evasion. Fingerprint posture is realistic-not-evasive (ADR 0009).

### R3 — Dead end with no human available · likelihood Med · impact Med
The chain is exhausted, the app hard-blocks, the flow is unrecognised, or the agent is stuck — and
no operator answers the `needs_human` handoff.
**Mitigation:** every dead end escalates the same way with a typed `reason`; the job surfaces
`needs_human{challenge_id, view_url, reason}` with an `expires_at`; on timeout it fails cleanly with
`handoff_timeout` (no silent spin), the per-account lock releases, and the operator can retry
(invariant 9: every dead end has an exit).

### R3b — Remote handoff view exposes an authenticated browser · likelihood Low · impact High
A remote DevTools/screencast `view_url` is full control of a logged-in browser.
**Mitigation:** `view_url` is **localhost-bound by default**; when exposed beyond the host it carries
a **one-time token** embedded in the URL that is revoked when the challenge is resolved or the job
ends; the trace records every handoff.

### R4 — App data sent to third-party LLM providers · likelihood High · impact Med
Perception/planning may ship page content (possibly the operator's PII) to an external model.
**Mitigation:** redaction on all external-bound payloads; **local-only mode** (route all tasks to a
local model) for sensitive apps; `config_hash` + audit record what model saw what; operator consent
is explicit per target.

### R4 — App data sent to third-party LLM providers · likelihood High · impact Med
Perception/planning may ship page content (possibly the operator's PII) to an external model.
**Mitigation:** redaction on all external-bound payloads; **local-only mode** (route all tasks to a
local model) for sensitive apps; `config_hash` + audit record what model saw what; operator consent
is explicit per target.

---

## Reliability

### R5 — Login flow variety / brittle auth · likelihood High · impact High
Real apps vary wildly (multi-step, modal, OAuth, magic-link, OTP, TOTP); a brittle login fails the
whole run.
**Mitigation:** per-variant `FlowHandler`s with deterministic selection, deterministic **success
detection** (§6.3), and form-error recovery; measured auth success rate per app in the benchmark lab.

### R6 — A11y handle instability under SPA re-render · likelihood High · impact High
Churning node handles break the planner's references mid-flow.
**Mitigation:** semantic handle anchoring (role+name+structural path), re-resolve every snapshot,
inter-step diffing; **spike before M3**; vision escalation as recovery.

### R7 — Weak/local models fail structured output · likelihood High · impact High
The "model-agnostic down to local" promise breaks if the ladder is flaky.
**Mitigation:** structured-output degradation ladder with validate+repair and grammar-constrained
decoding on local endpoints; schemas in the strictest common subset; measured local success rate.

### R8 — (Phase 2) Crawl loops / gets stuck · likelihood High · impact Med
Autonomous browsing repeats actions or thrashes on lists. **Phase-2 only** — v1 doesn't crawl.
**Mitigation:** simhash **state dedup** (collapse `/item/{id}`), the verifier's stuck/error/logout
detection, a per-run step budget, and the simple stop condition (no new states for N iterations).

### R9 — SPA timing / races · likelihood High · impact Med
Acting before the app settles misfires.
**Mitigation:** `WaitStable` = network-idle + DOM-mutation quiescence + route stability, bounded by
budget; flow handlers verify field population before submit.

### R10 — Verification latency / operator unavailable · likelihood Med · impact Med
An OTP/magic-link may take time, or the operator may not answer.
**Mitigation:** `VerificationProvider` bounded by `since`/`deadline`/`ctx`, **resend** support, and
the **async MCP pattern** (`needs_verification`/`resolve_challenge`) so a run doesn't hang a
worker; built-in TOTP is instant. Timeouts fail cleanly and are audited.

### R11 — Session expiry during/after auth · likelihood Med · impact Med
A token expires between login and handback, or the bundle a caller receives is already near expiry.
**Mitigation (v1):** parse JWT `exp` and include it in the bundle so the caller knows the window;
success detection runs just before handback. (Long-lived re-login is a **Phase-2** concern — auto
re-login on mid-crawl expiry.)

### R12 — (Phase 2) simhash threshold wrong · likelihood Med · impact Med
Too tight re-explores forever; too loose merges distinct views. **Phase-2 only.**
**Mitigation:** configurable Hamming threshold; **spike before M5** against apps with known state counts.

### R13 — Bot detection blocks the honest browser · likelihood Med · impact Med
Strict bot management challenges even a realistic browser.
**Mitigation:** realistic **persistent per-account profiles** (a returning-user profile reduces
challenges) + human-like pacing (ADR 0009); human-in-the-loop on challenge; if persistently
blocked, pause and let the operator decide — not an evasion arms race.

---

## Cost

### R14 — LLM spend runs away · likelihood Med · impact High
Vision escalation, long browse runs, or a chatty planner blow the budget.
**Mitigation:** Budget enforced at loop top, before every LLM call, and within every solver chain
(tokens + USD + solver cost/count + time + requests); deterministic-first keeps model calls rare;
per-task routing sends cheap work to cheap/local models; vision is escalation-only; kill-switch.

---

## Security of the tool itself

### R15 — Credential / session vault compromise · likelihood Low · impact High
The vault holds operator credentials and live sessions.
**Mitigation:** encrypted at rest (age/NaCl secretbox; key from OS keyring or passphrase); no
secrets in plaintext columns; optional encrypted volume/SQLCipher; per-account DB files deletable
when done.

### R16 — Handback (SessionBundle + AuthRecording) leaks secrets · likelihood Med · impact High
The bundle holds cookies/tokens/auth headers and the **AuthRecording holds request/response bodies
incl. the password and OTPs** — sensitive by design (ADR 0016).
**Mitigation:** both are returned **only to the authenticated caller**, **redacted in the audit log**
(never stored there in clear), and kept only in the per-account workspace; the recording **flags
secret fields** so the caller handles them carefully; the proxy/IP-binding note warns about reuse.

### R17 — Malicious app content → the agent/host (incl. prompt injection) · likelihood Med · impact Med
App pages are untrusted; page text could try to coax the model into an unsafe action.
**Mitigation:** the LLM can only emit **typed actions**, never code; page/DOM text is treated as
untrusted data in prompts (delimited, never instructions); browser sandboxed; downloads quarantined,
never executed. (v1 has no scope/deny-list to lean on, so the typed-action constraint + the narrow
auth-flow task are the containment; Phase-2 adds the account-safety deny-list.)

### R18 — Audit log tampering / gaps · likelihood Low · impact High
A compromised or buggy log undermines the record of what the agent did.
**Mitigation:** append-before-act (a failed append trips the kill switch); hash-chained entries
verified on load; a break marks the run `integrity_suspect`; redaction applied before write.

### R19 — Supply-chain / dependency risk · likelihood Low · impact Med
**Mitigation:** minimal core dependencies; pinned modules + checksum db; CAPTCHA solver plugins run
out-of-process over gRPC (isolated), and an external plugin only ever receives a `Challenge`, never
the page or session.
