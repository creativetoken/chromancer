# ADR 0009 — Fingerprint & Bot-Detection Posture: realistic, not evasive

**Status:** Accepted · **Date:** 2026-11-15

## Context
Apps run bot detection (reCAPTCHA v3 scoring, Cloudflare Bot Management, Turnstile,
DataDome-style). The agent needs to behave like a real browser enough to **use an app the
operator is authorised to use**, but we are not building a detection-evasion / anti-bot-bypass
toolkit — that's outside this tool's job and depends on authorisation it can't assume.

## Decision
Configure a **realistic, stable, operator-declared browser identity**, and stop there:

- **Realistic defaults a real user would have:** genuine Chromium UA matching the actual
  binary, a normal viewport, the operator's timezone/locale, and a **persistent profile**
  per identity (so sessions, cookies and storage behave like a returning user).
- **Human-plausible pacing:** actions respect a per-target rate limit and use modest,
  configurable inter-action delays — primarily to behave like a real user, not to defeat
  timing analysis.
- **No evasion arsenal:** we do **not** ship fingerprint spoofing/randomisation, headless-
  detection patches, `navigator.webdriver` hiding beyond running a normal headful browser,
  residential-proxy rotation, or CAPTCHA-score manipulation. If an app hard-blocks our honest
  browser, that's a signal to pause and let the operator decide, not to escalate evasion.
- **Headful by default** (ADR 0002) — the most "real" configuration and the most auditable.

## Rationale
- The agent simply identifies as a normal browser using an app the operator is authorised to
  use; aggressive evasion would turn a benign automation tool into an anti-bot-bypass toolkit,
  which is out of scope and undermines the clean audit story.
- Persistent realistic profiles solve the *actual* problem (being usable as a returning
  logged-in user) without an evasion stack.
- Everything we do is something a normal user's browser does, which keeps the audit trail honest.

## Consequences
- Some apps with strict bot management will challenge or block us; the correct responses are the
  CAPTCHA subsystem's human-in-the-loop path (ADR 0008) and, if persistently blocked, pausing
  and letting the operator decide — not an evasion feature.
- Operators who want a specific configuration (e.g. their own proxy/harness or a particular UA)
  set it in config; these are declared and audited, not hidden heuristics.

## Rejected alternatives
- **Full anti-detect / stealth stack:** out of scope for an authenticate-and-browse tool and a
  maintenance arms race. Rejected.
- **Headless with webdriver-hiding patches:** trips the exact "naive bot" signals detectors
  look for and reads as evasion; headful-realistic is both safer and more robust.
