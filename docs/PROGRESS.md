# PROGRESS

> Single source of truth for resuming. On restart: read this file and `DECISIONS.md` first, then
> continue from **Next** without redoing finished work.

## Current milestone
**M5 — Phase 2 authenticated crawl** (verifying full e2e). M1–M4 complete + tagged.

## M5 done
- `explore` package: BFS **Explorer** from a saved session — navigate, dedup by simhash
  (parameterised routes collapse), submit a representative form with sensible typed data,
  plateau/budget stop. **Account-safety deny-list** at the action boundary (logout, delete
  account, change creds, revoke, API keys) — controls are skipped and recorded, never clicked.
  **Mid-crawl re-login** on session loss (same browser), restarting the in-flight sub-goal.
- `app.RunCrawl` (restore session → Explorer with same-browser Authenticator relogin); CLI
  `agentbrowser crawl`; MCP `start_crawl` job (report in `get_job` done).
- Lab `crawl` app (nav menu, paginated list, item detail, settings form, logout link,
  delete-account button, session expiring after N requests) + ground-truth states.
- e2e: full login→crawl reaches all ground-truth states, submits the form, refuses logout/
  delete, survives forced expiry via re-login, stops on plateau/frontier; MCP start_crawl too.

## M4 done
- `captcha` package: `Detector`/`Injector`/`Resolver`/`Solver` contracts; per-vendor
  **detectors** (mock + reCAPTCHA v2/v3, hCaptcha, Turnstile via DOM/script/sitekey signatures);
  generic token/UIActions **injector**; **Resolver** (Describe-prefiltered chain, budget, fresh
  challenge per retry, Outcome→Report); **HumanHandoff** (headful / localhost-or-token remote
  view + notify + handoff timeout).
- Solvers: in-process **testmode** + **vision** (via router, mock-tested); **out-of-process
  plugin** over a stdio-JSON transport mirroring `proto/solver.proto` (see DECISIONS re: gRPC);
  **mocksolver** binary; **conformance suite** (`plugin.Conform`) run against in-proc + subprocess.
- Wired into the auth flow: pre-submit `Captcha` hook + resolve-loop outcome reporting + stuck/
  unknown-flow escalation to handoff; bridged through `app.RunAuth` and the JobManager
  (`needs_human` → `resolve_challenge(done)`).
- Lab: captcha-mock, captcha-block, stuck-login. e2e: testmode + gRPC-style plugin solve,
  block→human-handoff (direct + MCP), handoff timeout, stuck→handoff. All passing individually;
  full suite running (task b3xnt1vho).

## M3 done
- `verify` package: `Provider` interface + `Chain`; built-in **TOTP** (RFC 6238, no dep),
  **CLI prompt**, **MCP ask-bridge** (`AskProvider` → job prompter), **webhook**, **lab inbox**.
- `auth`: **signup flow handler** (operator profile / generated incidental fields; confirm-
  password; terms checkbox), **verification-step detector** (email/sms/totp code + magic-link,
  channel + masked hint), **account-exists → login**, and a **post-submit resolution loop**
  (verify-before-success, form-error recovery with no-progress guard, SSO-only fast-fail,
  signup→verify→login).
- Verification wired through `app.RunAuth` (TOTP + CLI or MCP-ask chain) and the JobManager
  (per-job `needs_verification` → `resolve_challenge`).
- Lab apps: signup-verify, magic-link, otp-sms, totp, form-errors, sso-only.
- e2e: all six M3 apps (CLI-style) + MCP async signup+OTP. Passing individually; full suite
  running (task b0itg9ix7).

## Done
- **Step 0 / 0.5 complete.** Scaffold, Makefile, CI, README, docs. Module
  `github.com/creativetoken/chromancer`.
- Foundational packages + unit tests: `core` (typed actions, budget/kill-switch), `audit`
  (hash-chained log, redaction), `identity` (secretbox credential store), `model` (LLMProvider,
  mock, OpenAI-compatible provider + structured-output ladder), `config`.
- `browser` (go-rod: persistent profile/account, optional proxy, frame-aware pages, WaitStable,
  session export/import, CDP network capture, Reload).
- `perceive` (a11y-first, stable integer handles across main frame + iframes, simhash-able
  signature, Compact serialisation) + unit tests.
- `handback` (HAR-style recorder w/ secret flagging + post-login capture window, replay checker,
  artefact writer) + unit tests.
- `cmd/authlab` + M1 lab apps: simple, multistep, modal, iframe, spa-jwt, hidden-entry,
  bound-session, each with `/__lab/expect` ground truth + unit tests.
- **M1 auth**: entrypoint discovery (hints→cache→link/path/modal→LLM fallback), form + multistep
  flow handlers, deterministic success detection, session restore, Authenticator orchestration.
  CLI `agentbrowser` (creds put / auth / export-session / newkey). `internal/app` wiring.
- e2e: perception smoke test + full M1 login test across all 7 apps + restore-without-login.
  Subset (modal, spa-jwt, bound-session) passing after two fixes (submit selection; post-login
  reload). **Full-suite run in progress** (task bj6pwsjz7).

## Next
- Confirm full M1 e2e green → tag `m1` → **M2** (JobManager + MCP server: start_auth/get_job/
  resolve_challenge/cancel_job/export_session; per-account lock; artefacts as resource URIs).

## Known issues
- **GitHub push blocked**: `github.com/creativetoken/chromancer.git` returns 404 (repo not created;
  no `gh` CLI to create it). Committing locally; retrying push per milestone. See DECISIONS.

## Deferred
- Set-of-marks vision escalation: interface + `NeedsVision` present; screenshot→vision wiring is a
  stub (no M1 app needs it). Real-provider adapters (Anthropic/Gemini) are M6.

## How to run
```sh
make build      # go build ./...
make test       # go vet + race unit tests (mock LLM, no browser)
make lint       # gofmt/goimports/vet
make lab        # run cmd/authlab on 127.0.0.1:8089
make e2e        # browser e2e vs lab (needs Chrome; AGENTBROWSER_E2E=1)

export AGENTBROWSER_KEY=$(go run ./cmd/agentbrowser newkey)
go run ./cmd/agentbrowser creds put --target http://127.0.0.1:8089/simple/ --account alice \
  --username alice@example.com --password 'correct horse'
go run ./cmd/agentbrowser auth --account alice --mode login
go run ./cmd/agentbrowser export-session --account alice
```
