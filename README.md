# chromancer (agentbrowser)

An **agent-driven browser** that gets authenticated and hands the session back. It signs up or
logs in to a web app with **operator-provided credentials**, handles verification/2FA and
CAPTCHAs (escalating to a human when blocked), then returns a **reusable session bundle** and an
**auth-flow recording**. Model-agnostic, Go, deterministic-first. Exposed as a **CLI** and a
**job-based MCP server**. Phase 2 adds an **authenticated crawl**.

The approved design is `docs/ARCHITECTURE.md`. Build status / how to resume is in
`docs/PROGRESS.md`; autonomous decisions are in `docs/DECISIONS.md`.

## Install

Prerequisites: **Go 1.27+** and **Google Chrome / Chromium** (go-rod downloads a browser if none
is found).

```sh
go install golang.org/x/tools/cmd/goimports@latest   # for `make lint`
make build
```

## Quick start

```sh
export AGENTBROWSER_KEY=$(go run ./cmd/agentbrowser newkey)   # credential/session encryption key

# run the offline mock-app lab (the e2e + demo target)
make lab                                        # serves on 127.0.0.1:8089

# store operator credentials, then authenticate and hand back a session
go run ./cmd/agentbrowser creds put --target http://127.0.0.1:8089/simple/ --account alice \
    --username alice@example.com --password 'correct horse'
go run ./cmd/agentbrowser auth --account alice --mode login \
    --login-url http://127.0.0.1:8089/simple/login
go run ./cmd/agentbrowser export-session --account alice

# Phase 2: crawl the authenticated app
go run ./cmd/agentbrowser crawl --account alice
```

## Configuration

Everything is flags + environment; there is no required config file.

| Env | Meaning |
|---|---|
| `AGENTBROWSER_KEY` | **required** — base64 of 32 random bytes; encrypts creds + sessions. `agentbrowser newkey` prints one. |
| `AGENTBROWSER_WORKSPACE` | per-account workspace dir (default `.workspace`). |
| `AGENTBROWSER_HEADFUL=1` | run Chrome with a visible window (needed for in-person human handoff). |
| `AGENTBROWSER_PROXY` | optional upstream proxy URL for the browser (operator's harness). |
| `AGENTBROWSER_CHROME` | explicit Chrome/Chromium binary path. |
| `AGENTBROWSER_LLM` | `mock` (default when no key), `openai`, `anthropic`, `gemini`. |
| `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `OPENAI_MODEL` | OpenAI-compatible endpoint (also Ollama/vLLM). |
| `ANTHROPIC_API_KEY` / `ANTHROPIC_MODEL` · `GEMINI_API_KEY` / `GEMINI_MODEL` | real providers. |
| `AGENTBROWSER_SOLVER_PLUGIN` | path to an out-of-process CAPTCHA solver plugin binary. |

Deterministic skills drive the common path; an LLM is only a judgment fallback (e.g. entrypoint
discovery), so **everything works offline with the mock model**. Real providers are used only
when their key is set.

## CLI

```
agentbrowser creds put --target URL --account NAME --username U --password P [--totp-seed S]
agentbrowser auth  --account NAME --mode login|signup [--login-url URL] [--signup-url URL] [--headful]
agentbrowser export-session --account NAME [--bundle]
agentbrowser crawl --account NAME [--headful]
agentbrowser eval  [--runs N]        # benchmark the configured model against the lab
agentbrowser mcp                     # run the MCP server over stdio
agentbrowser newkey
```

`agentbrowser eval` benchmarks the configured model against every lab auth scenario. Example
(mock model, 1 run each):

```
SCENARIO                   MODEL       OK%   avg_ms   steps   tokens   cost_usd
bound-session/login        mock       100%    14946     4.0        0     0.0000
captcha-mock/login         mock       100%    14845     4.0        0     0.0000
form-errors/login          mock       100%    16804     6.0        0     0.0000
hidden-entry/login         mock       100%    14848     4.0        0     0.0000
iframe/login               mock       100%    14551     3.0        0     0.0000
modal/login                mock       100%    15536     4.0        0     0.0000
multistep/login            mock       100%    16584     5.0        0     0.0000
otp-sms/login              mock       100%    16621     5.0        0     0.0000
signup-verify/signup       mock       100%    16719     5.0        0     0.0000
simple/login               mock       100%    15618     4.0        0     0.0000
spa-jwt/login              mock       100%    15146     3.0        0     0.0000
totp/login                 mock       100%    16650     5.0        0     0.0000
```

(tokens/cost are 0 for the mock model; real numbers appear when `AGENTBROWSER_LLM` points at a
real provider and the LLM judgment fallback is exercised.)

`auth` prints `done{bundle_ref, recording_ref, trace}` or `failed{reason}` (reasons:
`sso_only`, `handoff_timeout`, `verification_timeout`, `flow_failed`, …). Artefacts are written
to the per-account workspace and returned **by reference**, never inline.

## MCP server

`agentbrowser mcp` speaks the Model Context Protocol over stdio (line-delimited JSON-RPC 2.0).
Tools:

- `start_auth(target, account, mode, login_url?, signup_url?)` → `{job_id}`
- `start_crawl(account)` → `{job_id}`
- `get_job(job_id)` → `running | needs_verification{challenge_id, channel, hint, expires_at} |
  needs_human{challenge_id, view_url, reason} | done{...} | failed{reason}`
- `resolve_challenge(job_id, challenge_id, code|link|approved|done)` — answers verification **and**
  human-handoff challenges (one verb)
- `cancel_job(job_id)`, `export_session(account)`

Artefacts (bundle, recording) are MCP **resources**; read them with `resources/read` on the
`*_ref.uri`. A **per-account lock** serialises operations on one account.

## Writing a CAPTCHA solver plugin

A solver only solves — it never touches the browser (the core applies the result via a per-vendor
Injector). Implement `captcha.Solver` and serve it over stdio with `plugin.Serve`:

```go
package main

import (
    "context"; "os"
    "github.com/creativetoken/chromancer/internal/captcha"
    "github.com/creativetoken/chromancer/internal/captcha/plugin"
)

type mySolver struct{}
func (mySolver) Name() string { return "my-solver" }
func (mySolver) Describe() captcha.Capabilities {
    return captcha.Capabilities{Vendors: []captcha.Vendor{captcha.VendorRecaptchaV2}, Kinds: []captcha.Kind{captcha.KindInteractive}}
}
func (mySolver) Solve(ctx context.Context, c captcha.Challenge) (captcha.Result, error) {
    return captcha.Result{Kind: captcha.ResultToken, Token: "..."}, nil
}
func (mySolver) Report(ctx context.Context, solveID string, accepted bool) error { return nil }

func main() { _ = plugin.Serve(mySolver{}, os.Stdin, os.Stdout) }
```

Build it and point `AGENTBROWSER_SOLVER_PLUGIN` at the binary. The normative wire contract is
`internal/captcha/plugin/proto/solver.proto`; the Go transport mirrors it (see `docs/DECISIONS.md`
for why the reference transport is stdio-JSON rather than gRPC). Every plugin must pass
`plugin.Conform`.

## Writing a VerificationProvider

Implement `verify.Provider` to source codes/links/approvals (the agent asks; your tool answers):

```go
type Provider interface {
    Supports(ch verify.Channel, account string) bool
    GetCode(ctx context.Context, req verify.Request) (verify.Result, error) // Code | Link | Approved
    Resend(ctx context.Context, req verify.Request) error
}
```

Built-ins: `TOTP` (local, from a seed), `CLI` (prompt), `Webhook`, `Ask` (MCP pending/answer),
and `LabInbox` (tests). `verify.Chain` tries providers in order by channel.

## Security notes on artefacts

The **SessionBundle** (cookies, storage, observed `Authorization`/CSRF headers, token expiry) and
the **AuthRecording** (HAR-style request/response of the auth flow + a bounded post-login window,
**bodies included, secrets flagged**) contain live credentials/tokens. They are:

- returned **only to the authenticated caller**, as file paths / MCP resource URIs (never inline);
- written only inside the per-account workspace (files are `0600`);
- **redacted before every audit-log write** — the audit log (hash-chained, tamper-evident) never
  contains plaintext passwords or tokens.

Credentials and saved sessions are encrypted at rest (NaCl secretbox; key from `AGENTBROWSER_KEY`).
The `replayable` flag on a bundle tells a consumer whether a plain-HTTP replay works or a browser
is still required (e.g. JS-computed per-request headers, IP-bound tokens).

## Testing

```sh
make test     # unit tests (mock LLM, no browser), -race
make e2e      # browser e2e vs the lab (AGENTBROWSER_E2E=1; needs Chrome)
make lint     # gofmt / goimports / vet
```

No real third-party sites, paid services or secrets are used in tests. Real LLM providers and
external CAPTCHA solvers are exercised only when their env vars are set.

## Layout

`cmd/agentbrowser` (CLI/MCP), `cmd/authlab` (lab), `cmd/mocksolver` (sample plugin),
`internal/{server,core,skills,perceive,browser,model,auth,verify,captcha,handback,identity,
audit,config,explore,eval,lab}`. See `docs/ARCHITECTURE.md` §10.
