# ADR 0016 — Session handback: SessionBundle + AuthRecording (the v1 product)

**Status:** Accepted · **Date:** 2026-11-15

## Context
v1's deliverable is not "the agent is logged in" — it's **handing a reusable, inspectable
authenticated session back to the calling agent**. A bundle of cookies alone is often not enough:
modern apps carry auth in a bearer header held only in JS memory, or need a CSRF token or a custom
header on every XHR. And the caller frequently needs to *understand* how auth works, not just
replay it blindly. So the handback has two artefacts.

## Decision
On success, `Authenticator.Run` returns a **`SessionBundle`** and an **`AuthRecording`**, plus a
status/trace.

**SessionBundle — everything needed to replay the session:**
- **Cookies** with domain/path/flags (Secure, HttpOnly, SameSite, expiry).
- **localStorage** and **sessionStorage** snapshots.
- **Auth headers observed on post-login XHR/fetch** — `Authorization`/bearer, CSRF tokens, custom
  headers — captured from traffic because they frequently live only in JS memory and never appear
  in cookies/storage.
- **User agent** used and **proxy** used (IP binding may matter for token reuse).
- **Token expiry** where parseable (JWT `exp`).
- **`replayable: true|false`** plus a reason when false (replayability check, below).

**AuthRecording — how auth works, so the caller can see and replay it:**
- A **HAR-style JSON** of **every request/response during the signup/login flow**, plus a
  **bounded post-login capture window**: after success detection the agent loads the landing page
  once (the post-login redirect URL, else the root) and records until **N authenticated requests**
  or **T seconds** (configurable, e.g. N=20, T=10s) — so there is always something to capture even
  when login itself produced no follow-up XHRs.
- **Bodies included**; **secrets flagged** (password fields, tokens, OTPs marked) so the caller can
  handle them carefully.
- This is a **scoped capture of the auth flow only** — a deliberate, bounded window — **not** a
  general traffic-capture or endpoint-discovery engine (that's explicitly out of scope).

**Replayability check (before returning):** the calling agent wants to reuse the authenticated XHR
flow, but some apps bind a session to the browser itself. The agent picks one captured
authenticated XHR (GET preferred, e.g. the `/me`-style request) and **replays it with a plain Go
HTTP client using only the bundle's cookies and headers**. Same status + equivalent body ⇒
`replayable: true`; otherwise `false` with a reason — TLS/client fingerprinting, request signing,
DPoP / proof-of-possession tokens, per-request headers computed by JavaScript, or an IP/proxy-bound
session. This tells the caller whether plain HTTP will work or it must keep driving a browser.

**Artefacts by reference:** both artefacts are **written to the per-account workspace** and the job
returns **references** — file paths for the CLI, MCP resource URIs for MCP — never inline payloads
(they are large and contain secrets).

**Trace:** entrypoint used and how it was found, flow variant used, verification/CAPTCHA steps,
whether and why human handoff was needed, the replayability result, and timings.

**Sensitivity:** both artefacts contain live credentials/tokens. They are returned **only to the
authenticated caller**, **redacted in the audit log** (never written there in clear), and stored
only within the per-account workspace. `export_session` returns a reference to the latest bundle.

## Rationale
- Cookies-only handback silently fails on bearer/CSRF-header apps; capturing the post-login auth
  headers is what makes the bundle actually reusable.
- The recording turns "it logged in" into "here is exactly how it logged in," which is what a
  downstream agent needs to integrate or debug — far more useful than a success boolean.
- Bounding the recording to the auth flow (+ a few authed XHRs) keeps it from drifting into the
  recon/discovery scope we deliberately removed, and keeps the artefact small.

## Consequences
- The browser layer exposes a CDP network subscription the `AuthRecorder` consumes; auth-header
  capture reads request headers on post-login XHRs.
- `MarkAuthenticated` (called by success detection) starts the "first few authed XHRs" window so the
  recording knows where "after login" begins.
- Secret-flagging needs a redaction pass over bodies/headers; the same redactor backs the audit log.
- Bundle/recording handling is the main data-sensitivity risk (RISKS) — hence the return-to-caller-
  only + redact-in-logs rule.

## Rejected alternatives
- **Cookies + storage only:** breaks on bearer-in-JS-memory and CSRF-header apps. Rejected.
- **Full traffic capture / HAR of the whole session:** that's the recon scope we removed; the
  recording is bounded to the auth flow + a few authed XHRs.
- **No recording (bundle only):** loses the "see and replay how auth works" value that is the point
  of handing back to another agent.
