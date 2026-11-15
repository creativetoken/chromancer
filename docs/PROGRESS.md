# PROGRESS

> Single source of truth for resuming. On restart: read this file and `DECISIONS.md` first.

## Status: **COMPLETE** — M1–M6 built, tested, tagged (m1…m6).

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
