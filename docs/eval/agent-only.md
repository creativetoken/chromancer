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
