# PROGRESS

> Single source of truth for resuming. On restart: read this file and `DECISIONS.md` first, then
> continue from **Next** without redoing finished work.

## Current milestone
**M2 — Job-based MCP server** (complete)

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
