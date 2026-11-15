# ADR 0014 — Verification via a pluggable `VerificationProvider` (agent asks, operator answers)

**Status:** Accepted · **Date:** 2026-11-15

## Context
Login and signup frequently require a second factor or confirmation: an email OTP, a magic link,
an SMS OTP, a TOTP authenticator code, or a push approval. The operator owns the accounts and the
places those codes/links arrive (their inbox, their phone, their authenticator). The old design
had the agent poll a mail inbox and hold a captured TOTP seed. That couples the agent to mail
infrastructure, doesn't cover SMS/push, and assumes the agent can read the operator's inbox.

## Decision
Replace mail-polling and seed-capture with **one pluggable interface the agent calls on demand**:

```
GetCode(ctx, Request{Channel: email|sms|totp|push, Account, Hint, Since, Deadline}) (Result, error)
Resend(ctx, Request) error
// Result = Code | Link | Approved   (Approved for push: nothing to enter)
```

The **agent detects which channel the page wants** and extracts a **hint** (e.g. "code sent to
***1234"). `Since`/`Deadline` bound the time window so a **resend** doesn't pick up a stale code;
`Resend` re-triggers delivery where the channel supports it. The result is a one-time **`Code`**,
a magic **`Link`**, or **`Approved`** (push). **The agent asks; the operator's tool answers.**

**Implementations (all behind the same interface):**
- **Built-in TOTP generator** — a supplied seed → code computed locally, returns immediately.
- **CLI prompt** — the agent pauses and the operator types the code/link (blocking).
- **Webhook** — the provider waits for the operator's system to POST the code/link.
- **Tool / MCP (async)** — over MCP the agent does **not** block: the `start_auth` job goes
  `needs_verification{challenge_id, channel, hint, expires_at}` and the harness answers with
  `resolve_challenge(challenge_id, code|link|approved)`, which the provider delivers into the
  waiting `GetCode`. The **same pending/answer pattern** serves the human CAPTCHA handoff (ADR 0015).

Calls are bounded by `ctx`/`Deadline` and audited (redacted).

## Rationale
- "Agent asks, operator answers" matches reality: the operator controls where codes land, and most
  operators would rather hand the agent a tool/prompt than give it inbox/SMS credentials.
- One interface spans every channel (email/SMS/TOTP/push) and every delivery mechanism (prompt,
  tool, webhook, local TOTP), so new channels are new implementations, not core changes.
- The built-in TOTP generator keeps the common MFA case fully automated when a seed is available,
  without the agent "capturing" anything — the operator supplies the seed like any other cred.

## Consequences
- The `Authenticator` detects channel + hint and calls the provider; it no longer owns an inbox
  poller or a seed-capture step.
- Interactive providers (CLI/webhook) introduce human latency; bounded by `Deadline` and surfaced
  to the operator (and, under MCP, returned as a "waiting for verification" status).
- Multiple providers can be registered and matched by `Supports(channel, account)`; config sets
  which provider serves which channel per target/account.

## Rejected alternatives
- **Agent polls a mailbox (old MailProvider):** needs inbox access, misses SMS/push, couples the
  agent to mail infra. Replaced.
- **Capture the TOTP seed from enrolment:** the agent no longer enrolls accounts; the operator
  supplies the seed if they want local TOTP. Removed.
- **Hard-code per-channel handling in the Authenticator:** not extensible; the pluggable interface
  keeps channels and delivery mechanisms open.
