# Agent-only validation (vs the deterministic baseline)

Branch: `feat/agent-only` · 2026-11-21 · backends **chromium** + **camoufox**

Compare with the deterministic baseline in [`pre-agent-only.md`](pre-agent-only.md).

## Method

With the agent-only driver (`AuthDriverAgent`, ADR 0017) the LLM agent decides
every step. A real LLM provider is required at runtime; the test suite drives the
**exact same loop** with the deterministic, test-only **lab policy** model
(`internal/lab/policy.go`, guarded by `testing.Testing()`), which reads each
OBSERVATION and returns one action. This measures the agent-only *path and tools*
(entry, fill, interstitials, verification, CAPTCHA, success gating, escalation)
end to end without a paid key; it does not measure a real model's token/cost.

Every scenario is asserted to:

- authenticate only via the `check_logged_in` gate (cookie/XHR detection);
- drive through the agent (`EntrypointHow == "agent"`, no deterministic
  entry-finder/handlers/resolve loop);
- never leak a password/code/seed into the model input.

## Auth scenarios — agent-only, lab policy (e2e)

All pass on **both** backends. `✓` = login confirmed via `check_logged_in`.

| scenario | what it exercises | chromium | camoufox |
|---|---|:--:|:--:|
| simple | plain login | ✓ | ✓ |
| multistep | email step → password step | ✓ | ✓ |
| modal | form behind a header "Sign in" modal | ✓ | ✓ |
| iframe | login form inside an iframe | ✓ | ✓ |
| spa-jwt | SPA, JWT in storage | ✓ | ✓ |
| hidden-entry | login reachable via a members link | ✓ | ✓ |
| bound-session | server-bound cookie session | ✓ | ✓ |
| form-errors | transient error on first submit, succeeds on retry | ✓ | ✓ |
| hydration-login | handles re-render under hydration | ✓ | ✓ |
| takeover-trust | "trust this device?" interstitial | ✓ | ✓ |
| takeover-terms | terms checkbox + continue | ✓ | ✓ |
| takeover-injection | prompt-injection page; deny-list blocks "Delete account" | ✓ | ✓ |
| otp-sms | password → SMS OTP (lab inbox) | ✓ | ✓ |
| magic-link | passwordless email magic link | ✓ | ✓ |
| totp | password → TOTP MFA (known seed) | ✓ | ✓ |
| combined-verify | password → email code | ✓ | ✓ |
| signup-verify | signup → email code → login | ✓ | ✓ |
| takeover-resend | code delivered only after "Resend code" | ✓ | ✓ |
| captcha-mock | login guarded by a CAPTCHA (testmode solver) | ✓ | ✓ |

Negative cases (must NOT authenticate), both backends:

| scenario | expected | chromium | camoufox |
|---|---|:--:|:--:|
| wrong-password | `credentials_rejected`, no retries | ✓ | ✓ |
| bot-wall (no human) | fail; agent asks for a human, never clicks through | ✓ | ✓ |

## Comparison to baseline

| metric | deterministic baseline | agent-only (lab policy) |
|---|---|---|
| auth success rate | 100% (12 eval scenarios, chromium) | 100% (21 e2e scenarios, chromium **and** camoufox) |
| driver | entry-finder + handlers + resolve loop | one agent loop; deterministic code is tools |
| secret leakage | n/a | none (asserted every scenario) |
| crawl | bfs 100%, hybrid 100%, llm-with-mock 20% | single llm loop (`--driver` deprecated) |

**Success rate does not drop on any scenario** — the agent-only path covers the
whole baseline surface and adds the interstitial/bot-wall/injection cases, on both
backends.

## Pending: eval with a real provider

Token/cost/latency numbers for the agent-only path need a real model (OpenAI-
compatible or Ollama) configured via `AGENTBROWSER_*` and the eval harness wired
to set `AuthDriver = AuthDriverAgent`. The lab-policy e2e above is the offline
correctness bar; the paid eval is a follow-up once a key is available.
