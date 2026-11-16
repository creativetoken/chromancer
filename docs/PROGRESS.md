# PROGRESS

> Single source of truth for resuming. On restart: read this file and `DECISIONS.md` first.

## Active work: **Verification module (V1–V5)** — the agent fetches 2FA codes/links itself (Twilio SMS, Gmail IMAP, TOTP incl. enrolment capture). CLI/MCP stay as fallbacks.
- [ ] **V1 — identity resources + config + shared extraction/claiming.** YAML resources (phones,
  mailboxes, providers, accounts, totp), startup validation (fail fast, never print secrets);
  deterministic code + link extractors; claimed-message store.
- [x] **V2 — Twilio SMS provider** (REST poll + signed webhook, matching, extraction, claim,
  resend, phone_rejected) vs an httptest mock Twilio API.
- [x] **V3 — Gmail IMAP provider** (shared conn per mailbox + NOOP health check + retry, junk-folder
  auto-discovery, narrow search, alias separation, MIME, code/link, claim-once) vs an in-memory IMAP
  server. Modelled on `ai-hackbot/internal/mailbox`. OAuth2 XOAUTH2 (default for Gmail) +
  app_password + password modes; `agentbrowser mailbox login` (loopback+PKCE, refresh token stored
  encrypted); config rejects `password` for Gmail.
- [x] **V3b — TOTP provider** (otpauth parsing, SHA1/256/512, 6/8 digits, 30/60s, boundary guard,
  skew retry, CurrentCode tool, QR decode for enrolment). *Still to do: per-digit input boxes +
  method picker + full enrolment-capture flow + lab app + e2e.*
- [~] **V4 — wiring.** Chain builder (TOTP→Twilio→IMAP) from resources; `verify test` and
  `mailbox login` CLI commands. *Still to do: Authenticator channel routing + fallback chain + MCP/LLM
  read_sms/read_email tools.*
- [~] **V5 — live smoke tests + `make live` (read-only, prints only counts/timings).** Docs + tag pending.

### Live validation (no values recorded)
- `make live`: twilio-1 connected, 6 recent inbound, extractor found a code in 3/6 (329ms);
  twilio-2 connected, 0 recent inbound; Gmail skipped (no app password / OAuth client configured).
- `verify test`: acct-a/sms PASS (got a code) in 228ms; acct-b/sms FAIL (timed out, 0 recent inbound);
  acct-a/email PASS (app-password IMAP, got a code) in ~5s; totp UNSUPPORTED (no seed configured).
- Gmail auth is an app password in `GMAIL_IMAP_PASSWORD` (matches the ai-hackbot reference); the
  Gmail default is `app_password`. A `--debug` funnel (counts only) on `verify test` shows the
  per-folder fetched→aliasMatched→newer→extracted stages and surfaces IMAP login errors.

## CAPTCHA module: **complete** — C1–C5 built, tested, tagged `captcha-v1`. Automated solving is the norm; human handoff is the last resort.
- [x] **C1 — detection & parameter extraction.** Unified detector + pure classifier shared by a
  browser JS probe (computed visibility) and a pure-Go `x/net/html` probe. Classes: reCAPTCHA v2
  checkbox / invisible / v3 / Enterprise, hCaptcha, Turnstile (managed/invisible), Cloudflare
  interstitial, image-grid, text, Arkose/FunCaptcha + GeeTest (classify→handoff). Extracts sitekey,
  callback, action, enterprise, data-s, rqdata, cData, size. Detects from script signals and from a
  failed-submit response body. Fixtures in `testdata/captcha/fixtures/` (13 classes + 2 decoys);
  unit tests prove classification + extraction + **no false positives**. Score/invisible challenges
  are non-blocking. Lab captcha/handoff e2e stay green.
- [x] **C2 — injection & acceptance detection.** Injector sets every matching response field,
  invokes the page-registered success callback (named global from `data-callback`, incl. `obj.fn`,
  plus lab hooks), sets both h-captcha/g-recaptcha fields for hCaptcha, and types text answers
  (`ResultText`) into the text-captcha input. Acceptance gate: a token/text solve is "applied" only
  when the value is observed in an outgoing request (`Recorder.TokenSubmitted`, the reached-backend
  proof); accepted = applied AND challenge gone; otherwise rejected → the resolve loop re-detects a
  fresh challenge. Unit tests: `TokenSubmitted` (body + header, min length), `Result.Payload`.
  Full per-vendor browser proof lands in C4 with the vendor mocks.
- [x] **C3 — solvers.** Chain testmode → native → vision → external → human.
  - testmode answers vendor-published test sitekeys (reCAPTCHA v2/hCaptcha/Turnstile) + lab tokens; declines real keys.
  - native clicks the widget (Challenge.Rect) and the resolver waits for the response field to fill.
  - vision does text (ResultText) + image grid (UIActions, multi-round) with a confidence threshold and per-model accuracy.
  - external generic adapter `plugins/captcha-generic/` speaks create-task→poll→report over HTTP, forwards sitekey/URL/action/enterprise/UA/proxy, records cost, reports on Outcome; tested against a local mock service (no real service).
  - per-(target,vendor,solver) `Stats` (attempts/accepted/median/cost, file-backed) with optional `adaptive_order`.
  - YAML chain config (`configs/captcha.example.yaml`, `AGENTBROWSER_CAPTCHA_CONFIG`): chain, max_solves, max_cost, timeout_per_solver, adaptive_order, external[]. Conformance passes for testmode/native/vision/generic.
- [x] **C4 — lab vendor mocks + offline e2e.** `internal/lab/apps_captcha.go`: mock-recaptcha-v2
  (testmode), mock-recaptcha-invisible / mock-recaptcha-v3 / mock-turnstile-invisible (score,
  non-blocking), mock-hcaptcha (testmode), mock-turnstile-managed (native), text-captcha + image-grid
  (vision, ground truth at `/__lab/truth`), captcha-hard (→ handoff → resolve → success),
  captcha-on-failed-submit (widget appears after first reject). Lab is fully offline (no real vendor
  CDN scripts). Mock external solver service is a local httptest server in the generic adapter's test.
  e2e (`e2e/captcha_vendors_e2e_test.go`): every app solved by its chain; captcha-hard handoff;
  budget exhaustion → `failed{budget_exhausted}`. Found+fixed two design gaps: captchaHook now
  advances through the chain on a decline (handoff only on exhaustion), and native declines cleanly
  when its click produces no token.
- [x] **C5 — eval + docs + polish.** `agentbrowser eval --captcha` runs the mock vendor apps through
  the real chain and prints per-`(vendor × solver)` solve rate / median latency / cost from the
  persisted `Stats` (vision rows need a real vision model; offline they show 0% as expected).
  `docs/CAPTCHA.md` (detection, chain, config, writing a plugin, adding a vendor); ARCHITECTURE §6.6
  implementation note; README eval section. Outcome accounting fixed (accepted on authenticated,
  rejected on re-detected challenge — no mid-navigation race). Tag `captcha-v1`.

## v1 status: **COMPLETE** — M1–M6 built, tested, tagged (m1…m6).

All milestones from the autonomous build instructions are implemented, with unit tests (mock
LLM, `-race`) and browser e2e against the offline lab. `make lint test` and the full
`AGENTBROWSER_E2E=1` e2e suite are green.

## What was built
- **M1 — login + session handback (CLI):** go-rod browser (persistent profile/account, optional
  proxy), a11y-first perceiver (frame-aware, stable handles, simhash signature), entrypoint
  discovery (hints→cache→link/path/modal→LLM fallback), form/multistep flow handlers,
  deterministic success detection, session restore, AuthRecorder (HAR-style, secrets flagged,
  post-login window), SessionBundle, replayability check, artefact writer. CLI
  `creds/auth/export-session`.
- **M2 — job-based MCP server:** JobManager (per-account lock, lifecycle, cancel, pending/answer
  prompter), stdio JSON-RPC 2.0 MCP server (start_auth/get_job/resolve_challenge/cancel_job/
  export_session; artefacts as resources).
- **M3 — signup + verification:** signup handler (profile / generated fields), verification-step
  detection (email/sms/totp/magic-link + hint), account-exists→login, resolve loop (verify-first,
  form-error recovery, SSO-only fast-fail, signup→verify→login). VerificationProvider: TOTP (RFC
  6238), CLI, webhook, MCP ask-bridge, lab inbox.
- **M4 — CAPTCHA + human handoff:** per-vendor detectors (mock + reCAPTCHA v2/v3, hCaptcha,
  Turnstile), generic injector, budget-aware Describe-prefiltered Resolver, in-proc testmode/vision
  solvers, out-of-process plugin (stdio-JSON mirroring solver.proto) + mocksolver + conformance
  suite, HumanHandoff for any dead end (captcha/stuck/unknown-flow; localhost/one-time-token remote
  view; handoff timeout) bridged to MCP needs_human.
- **M5 — authenticated crawl (Phase 2):** BFS Explorer (simhash dedup, form exercise, plateau/
  budget stop), account-safety deny-list at the action boundary, mid-crawl re-login (same browser).
  CLI `crawl`; MCP `start_crawl`.
- **M6 — hardening & docs:** Anthropic + Gemini provider adapters (recorded + live-with-key tests);
  eval harness (`agentbrowser eval`) reporting success/steps/tokens/cost/time per model; expanded
  README (install, config, CLI/MCP, writing a solver plugin + VerificationProvider, security
  notes); dead-code removal; green lint + unit + e2e.

Lab apps (`cmd/authlab`, 17): simple, multistep, modal, iframe, spa-jwt, hidden-entry,
bound-session, signup-verify, magic-link, otp-sms, totp, form-errors, sso-only, captcha-mock,
captcha-block, stuck-login, crawl.

## Known issues / deferred
- **GitHub push pending**: `github.com/creativetoken/chromancer.git` returns 404 (repo not created;
  no `gh` CLI to create it). History + tags m1–m6 are committed locally and push in one go once the
  repo exists. (DECISIONS)
- **External CAPTCHA solver transport is subprocess stdio-JSON, not gRPC** (no protoc/buf in env);
  `solver.proto` is the normative schema and the Go transport mirrors it. (DECISIONS / ADR 0011)
- **Set-of-marks vision escalation** is wired behind its interface; the vision solver is
  mock-tested only (needs a real vision model).
- v1 persistence is file-based (not SQLite); fine for the small v1 data set (DECISIONS / ADR 0006).

## How to run
```sh
make build && make test            # build + race unit tests (mock LLM, no browser)
make lint                          # gofmt/goimports/vet
make lab                           # run the mock-app lab on 127.0.0.1:8089
make e2e                           # browser e2e vs the lab (needs Chrome)
export AGENTBROWSER_KEY=$(go run ./cmd/agentbrowser newkey)
go run ./cmd/agentbrowser eval     # benchmark the configured model vs the lab
```
