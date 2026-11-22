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

## 4. Live validation — icon-only sign-in (feat/icon-signin)

The perceiver now names unlabeled header controls and the prompt tells the agent
to open header account/avatar icons before escalating. Lab app `icon-signin`
(account icon → dropdown → form) passes agent-only on **chromium and camoufox**
(`TestE2EAgentPrimaryIconSignin`).

**Cold bambulab run (attempt 1/3):** the fix behaves correctly — the agent
reasoned *"the sign-in entry is likely an unlabeled account icon in the top-right
header"* and scrolled/waited to find it — but bambulab's account/sign-in control
is **not present in the perceivable DOM** for the automated browser (only a logo
and product links are listed). The agent escalated with `request_human` without
guessing URLs. Not retried: the control's absence is deterministic, not flaky.

**Follow-up (next blocker):** bambulab's header sign-in control is not in the
queried DOM — likely shadow DOM (the perceiver's `querySelectorAll` does not
pierce shadow roots) or a hydration/anti-automation gate (the homepage stalls at
`readyState: interactive`). Naming cannot help an element that is not in the DOM;
the next levers are shadow-DOM piercing in the perceiver and/or the camoufox
anti-detect backend. Tracked separately from the icon-naming work, which is
complete and lab-proven on both backends.

## 5. Visual perception (feat/visual-perception, 2026-11-22)

The agent now escalates to **visual mode** when the DOM can't be read (closed
shadow root, canvas, cross-origin iframe): it captures a viewport screenshot with
Go-drawn set-of-marks over the perceived nodes, sends it to a vision model, and
acts by mark number or raw coordinate (`click_at` / `type_text`). See
ARCHITECTURE §6.7.

### Plumbing / lab (deterministic, no model cost) — both backends

A test-only visual policy decodes the screenshot's marker colours and drives by
coordinate (a deterministic stand-in for a vision model), so the whole pipeline —
screenshot → set-of-marks → coordinate click/type into focus → auth — is proven
offline:

| lab app | what it exercises | chromium | camoufox |
|---|---|:--:|:--:|
| `shadow-login` | login form inside a **closed shadow root** (0 DOM nodes) | ✓ | ✓ |
| `canvas-signin` | sign-in control painted on a **`<canvas>`** | ✓ | skip¹ |

¹ camoufox: a coordinate `click_at` fires a form button's handler (so
`shadow-login` passes) but not a standalone over-canvas control's, and camoufox
ignores JS-initiated navigation — a backend event-dispatch quirk, orthogonal to
visual perception, documented in docs/CAMOUFOX.md. `shadow-login` (the harder,
representative case) proves visual mode on camoufox.

Full agent-primary lab e2e is **green on both backends** (chromium all pass;
camoufox all pass with `canvas-signin` skipped). Build/vet/lint + unit green.

### Live — bambulab.com

| run | mode | outcome | note |
|---|---|---|---|
| warm `/en-gb/sign-in` #1 | agent | restored (no login) | a healthy saved session was restored, so no fresh sign-in occurred; the restore confirms the account/credentials are healthy (fresh `refreshToken`/`token` cookies), but it did not exercise the login form |
| warm `/en-gb/sign-in` #2 | agent | **not run** | the fresh-login attempt (saved session moved aside to force a real sign-in) was **denied by the harness auto-mode classifier** as a destructive/unwanted action — an autonomous live external login with real credentials cannot be run from this session |
| fresh `/en-gb/sign-in` #3 (operator) | agent | **failed: handoff_timeout** | `screenshots=0`, `mode=text`, `stop=plateau`, 10 steps / 44k tokens. The agent called `fill_login_form` on step 1, got "no login form (password field)" (the sign-in form is unreadable in the DOM), and then **never escalated to visual mode** — it spiralled clicking the header icon and timed out at a human handoff. **Root cause + fix below.** |
| fresh `/en-gb/sign-in` #4 (operator) | agent | **failed: handoff_timeout (OTP entry)** | Much further this time: cookies dismissed, `fill_login_form → filled_login`, submit, then the **email-OTP step** appeared and the code **was fetched** (4× "email sent to …") — but `enter_verification` failed 4× with a "field clear" error. `screenshots=0` (the form was readable, so no escalation needed). **Root cause + fix below.** |
| fresh `/en-gb/sign-in` #5 (operator) | agent | **failed: handoff_timeout (OTP entry, "timed out")** | Same shape as #4 but the OTP error became "timed out" (3 attempts). The first OTP fix left the `Type` step bounded only by the whole ~20s verify budget, so a guarded field blocked there before the fallback ran. Fixed: each `TypeInto` step is now capped at ~3s (rodbackend parity) and the fallback uses the native value setter (React-safe). |

**Live-run #3 finding (fixed):** the failed step-1 `fill_login_form` set
`loginAttempted=true`, which the post-login guard in `wantVisual` used to suppress
all visual escalation — so on bambulab's unreadable form the agent could never
look. Fix: form-finding ends only on a fill/type that actually progressed
(`marksLoginDone`), never on a fill that found no form; and a "no form" fill now
flags `fillFoundNoForm` so visual mode escalates on the next step. See
docs/DECISIONS.md (2026-11-22) and `internal/explore/visual.go`. Unit-tested
(`marksLoginDone`, `fillFoundNoForm`, escalation); the deterministic lab policy
cannot reproduce the LLM's speculative fill (it only fills a perceivable form), so
coverage is at the unit level.

**Live-run #4 finding (fixed):** login worked end-to-end on camoufox and reached
the email-OTP step, but entering the code failed — the camoufox `cfPage.TypeInto`
hard-failed at `el.Fill("")` on the guarded code field and had no recovery (the
rodbackend path does: verify the value landed → un-guard with a real click → set
the value directly with input/change events). Fix: `cfPage.TypeInto` now mirrors
that fallback. Lab repro `combined-verify-keyguard` (code field blocks all real key
events) fails on the old camoufox typing and passes on the new — green on both
backends. The handoff noVNC link also now opens from the host (the script
publishes the in-container port).

**Live-run #5 finding (fixed):** the #4 fix recovered the clear-error but left the
`Type` step bounded only by the whole verify budget, so a guarded field made it
block (~20s) before the fallback ran — "type timed out". Fixed: every `TypeInto`
step (focus/clear/type/eval/click/set) is capped at ~3s, so the fallback always
gets to run; and the fallback now uses the element's **native value setter** so a
React/Vue-controlled input registers the code (a plain `el.value=v` is reverted by
such frameworks). `combined-verify-keyguard` green on camoufox.

**Important before the next live run:** runs #4 + #5 burned **7 OTP codes**.
bambulab likely throttles verification after that, which would make the next run
time out at code-fetch regardless of the fix — so **wait ≥2 h** before re-running
(the live-testing rule), and on the re-run **open the noVNC link** at any handoff
to see the OTP screen directly (code entered vs a "too many attempts" throttle).
**Re-run live (after the wait) to confirm OTP entry now lands.**

**Status:** the live cold/warm sign-in via visual mode is **not yet confirmed**.
The autonomous run cannot submit a live login (classifier denial); it must be run
by the operator (as the earlier live runs were). Run the committed script from the
repo root:

```sh
bash scripts/live-bambulab.sh
```

It moves the saved session aside to force a real sign-in and **restores it on ANY
exit** — success, failure, or Ctrl-C — via a trap, and the `agentbrowser` run
prints a **one-line summary** to stderr at the end
(`[auth] result=… driver=agent backend=camoufox mode=text+visual steps=… screenshots=… tokens=…/…`).
`mode=text+visual` means the run escalated to visual mode at least once; `mode=text`
means it never did (on the unreadable bambulab form it now should — see the
live-run #3 fix above). Running a committed script avoids the earlier paste
artifact (a dropped subshell `(` left a stray `mv` and could skip the restore); if
you prefer to paste the steps, include the whole subshell:

```sh
# Equivalent inline form — paste the ENTIRE block including the leading "(".
(
  set -u
  SESS=.workspace/accounts/bambulab/session.enc
  restore() { [ -f "$SESS.save" ] && mv -f "$SESS.save" "$SESS" && echo "[live] restored saved Bambu Lab session"; }
  trap restore EXIT INT TERM
  [ -f "$SESS" ] && mv "$SESS" "$SESS.save"

  # -p publishes the in-container human-handoff noVNC port to the host, and the
  # HANDOFF_BIND/ADVERTISE env make its printed URL openable from macOS.
  docker run --rm -it -p 127.0.0.1:8899:8899 -v "$PWD":/src -w /src agentbrowser-camoufox-test:latest bash -c \
   'set -a; . /src/.env.live; set +a; export AGENTBROWSER_BACKEND=camoufox AGENTBROWSER_WORKSPACE=/src/.workspace AGENTBROWSER_RESOURCES=/src/configs/resources.example.yaml PATH=/usr/local/go/bin:$PATH; \
    export AGENTBROWSER_HANDOFF_BIND=0.0.0.0:8899 AGENTBROWSER_HANDOFF_ADVERTISE=127.0.0.1; \
    Xvfb :99 -screen 0 1280x900x24 >/dev/null 2>&1 & export DISPLAY=:99; sleep 1; \
    go run ./cmd/agentbrowser auth --account bambulab --mode login --auth-driver agent --login-url https://bambulab.com/en-gb/sign-in'
)
```

If the run reaches a human handoff, it prints a `http://127.0.0.1:8899/vnc.html?…`
link (token-gated) — open it to see/drive the browser, then press Enter. Without
the `-p`/`HANDOFF_*` wiring the link points at an in-container port and won't open.

Expectation: on bambulab's unreadable sign-in form the agent should escalate to
visual mode (it does on the `shadow-login` repro) and drive the fields by
coordinate. The known live OTP-entry gap (§3) still applies; the `verify-shadow`
lab repro now exercises visual OTP entry end to end, but the live step is still
unconfirmed.
