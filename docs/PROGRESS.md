# PROGRESS

> Single source of truth for resuming. On restart: read this file and `DECISIONS.md` first.

## Active work: **CAPTCHA solving module (C1–C5)** — make automated solving the norm, human handoff the last resort.
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
- [ ] **C5 — eval (solve rate / time / cost per vendor×solver), `docs/CAPTCHA.md`, ARCHITECTURE §6.6 refresh, tag `captcha-v1`.**

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
