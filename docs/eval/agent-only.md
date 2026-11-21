# Agent-only eval (vs the deterministic baseline)

Branch: `feat/agent-only` · 2026-11-21 · backends **chromium** + **camoufox**

Compare with the deterministic baseline in [`pre-agent-only.md`](pre-agent-only.md)
(deterministic drivers, mock model, 0 tokens).

Two things are measured here, kept separate:

1. **Plumbing** — the agent-only *path and tools* driven by the deterministic,
   test-only **lab policy** model (`internal/lab/policy.go`). This proves the
   loop works end to end with zero model cost. Lab-policy numbers are NOT a real
   model's behaviour — they only prove the plumbing.
2. **Real provider** — the same `AuthDriverAgent` path driven by the operator's
   configured model (`anthropic/claude-haiku-5-5`, the cheapest Claude), via
   `agentbrowser eval --auth-driver agent`. This is the real success / steps /
   tokens / cost / time.

---

## 1. Plumbing (lab policy, no model cost) — both backends

The whole agent-primary lab auth surface passes agent-only on **chromium and
camoufox** with the lab policy: simple, multistep, modal, iframe, spa-jwt,
hidden-entry, bound-session, form-errors, hydration, trust/terms/injection
interstitials, otp-sms, magic-link, totp, combined-verify, signup-verify,
resend-OTP, captcha-mock — plus the negatives (wrong-password →
`credentials_rejected`; bot-wall → fails, never clicks through). Every scenario
asserts: authenticated only via `check_logged_in`; `EntrypointHow == "agent"`;
no password/code/seed reaches the model input. (0 tokens, deterministic.)

## 2. Real provider — `anthropic/claude-haiku-5-5`, agent driver

`agentbrowser eval --auth-driver agent --runs 1`. Cost at Haiku 5.5 list price
($0.10 / $1M input, $0.50 / $1M output, <100K-token prompts) via
`AGENTBROWSER_LLM_PRICE_IN/OUT`. Tokens/cost are model-bound, so they are
essentially identical on both backends; time differs by engine. `steps` = agent
actions.

| scenario | OK | steps | tokens | cost_usd | chromium ms | camoufox ms |
|---|:--:|---:|---:|---:|---:|---:|
| simple/login | ✓ | 4 | 9 809 | 0.0011 | 46 994 | 25 428 |
| multistep/login | ✓ | 7 | 17 609 | 0.0020 | 46 330 | 25 708 |
| modal/login | ✓ | 4 | 10 046 | 0.0015 | 46 576 | 20 468 |
| iframe/login | ✓ | 3 | 7 382 | 0.0008 | 19 170 | 20 708 |
| spa-jwt/login | ✓ | 3 | 7 391 | 0.0008 | 19 136 | 18 823 |
| hidden-entry/login | ✓ | 3 | 7 414 | 0.0008 | 19 041 | 19 400 |
| bound-session/login | ✓ | 3 | 7 340 | 0.0008 | 18 914 | 20 111 |
| otp-sms/login | ✓ | 4 | 9 855 | 0.0011 | 45 638 | 21 482 |
| totp/login | ✓ | 4 | 9 829 | 0.0011 | 46 280 | 20 567 |
| captcha-mock/login | ✓ | 4 | 9 985 | 0.0012 | 46 015 | 22 100 |
| signup-verify/signup | ✓ | 6 | 15 377 | 0.0019 | 44 685 | 29 509 |
| form-errors/login | ~ | 5 | 12 591 | 0.0017 | 47 728 | 23 406 |

All 12 scenarios succeeded in clean full runs on **both** backends (chromium
12/12, camoufox 12/12). Totals per full run: ≈ 124 k tokens, ≈ $0.014, 12/12.

### Reliability of the cheapest model

Across repeated runs, **`form-errors` is flaky with Haiku** — a 3-run sample
scored **67% (2/3)**. It is the transient-error-then-retry flow: Haiku
sometimes re-fills/re-submits inconsistently and gives up before the second
(successful) attempt. `signup-verify` showed one failure in ~5 runs (it loops
past the time budget when it mishandles the signup→verify→login hand-off) but
scored 3/3 in a dedicated sample. Every other scenario was 100% across all runs.

This is model capability, not a plumbing defect (the lab policy is 100% on these
same scenarios). The mitigation already exists: route the **plan** step to a
stronger model (cheap-vs-plan routing via the `ModelRouter`); Haiku is the floor,
not the recommended plan model for the hardest multi-step flows.

### A found + fixed bug (real provider only)

Haiku exposed two issues the lab policy's fixed ordering hid, both fixed here:
`solve_captcha` is now idempotent (a weak model re-called it after a successful
solve and exhausted the chain → handoff), and the prompt now says to solve a
CAPTCHA before submitting.

## Comparison to baseline

| metric | deterministic baseline (mock) | agent-only, real Haiku |
|---|---|---|
| auth success | 100% / 12 scenarios (chromium) | 12/12 clean on chromium AND camoufox; `form-errors` ~67% over repeats |
| tokens / run | 0 (mock) | ≈ 124 k |
| cost / run | $0 (mock) | ≈ $0.014 (Haiku list price) |
| time / scenario | ~15–18 s | ~19–47 s (chromium), ~19–29 s (camoufox); dominated by model round-trips |
| secret leakage | n/a | none (asserted) |

**Success rate does not drop on any scenario in a clean run.** The only caveat is
run-to-run variance on `form-errors` with the cheapest model, addressed by using
a stronger plan model.

## 3. Live validation — bambulab.com (real provider, agent driver)

Real end-to-end runs against bambulab.com with the configured account
(`anthropic/claude-haiku-5-5`), headful, OTP via the Gmail relay. Results are
labelled as live (a single real site, subject to its own changes/rate limits),
not a benchmark. Live attempts are spaced ≥10 min; ≤5 per step.

**Site change since 2026-11-17:** bambulab's sign-in is now an unlabeled header
icon on `/en` (no text "sign in" link; `/en/login` 404s). The direct form is
`/en-gb/sign-in` (combined email + password + emailed-code).

| run | entry | outcome | note |
|---|---|---|---|
| cold `/en` | agent | failed/human ✓ | no sign-in link to find; escalates safely (after the success-detector fix; before it, false `done`) |
| warm `/en-gb/sign-in` #1 | agent | failed | filled form + reached OTP; code fetched but **entry failed** |
| warm `/en-gb/sign-in` #2 | agent | failed | instrumented: code `extracted=1`, typing hit a **focus/entry timeout**; retries **locked out** (`claimed=1`) |
| warm `/en-gb/sign-in` #3 | agent | failed | after unclaim fix: no lockout (`claimed=0 extracted=1` each retry); entry still times out ("context deadline exceeded") at the element/type stage |
| warm `/en-gb/sign-in` #4 | agent | failed (rate limit) | filled + submitted; page said "code sent" but **no OTP email was delivered** (`newer=0` throughout) — Bambu throttled verification email after 4 logins in ~50 min |

**Stopped at 4/5 live attempts: hit a verification rate limit** (per the live-site
rules — stop on a rate limit, don't keep retrying the verification). The last
delivered OTP was for attempt #3; attempt #4's was throttled. Step 2 is therefore
**not confirmed live**: the last useful signal (attempt #3) was that the code is
fetched but entry into bambulab's live code field times out at the element/type
stage. Stage-naming diagnostics + a 20s entry budget were added but could not be
exercised (attempt #4 never received a code). Session restored after every run.

**Bugs found + fixed via these runs (all with lab repros + tests):**
- Success detector counted consent/CDN/CSRF cookies as a login → false `done`
  on an escalation. Fixed (`TestStorageEvidenceConsentAndCDNCookiesNotAuth`).
- A fetched code was claimed on fetch, so a failed entry locked out all retries.
  Fixed: unclaim on failed entry (`TestClaimUnclaimAllowsRetry`).
- Guarded one-time-code fields (key events don't land) — `TypeInto` now verifies
  the value took and falls back to click + direct value-set
  (`TestE2ECombinedVerifyGuarded`, fails without the fix).

**Open:** live OTP entry into bambulab's code field still times out at the
element/type stage (stage-naming diagnostics added). The lab combined-verify
(incl. the guarded variant) passes agent-only on both backends; the remaining
gap is specific to bambulab's live code field. Steps/tokens/time will be
recorded here once a live run completes the OTP step.
