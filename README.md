# agentbrowser

An **agent-driven browser** that gets authenticated and hands the session back. It signs up or
logs in to a web app with **operator-provided credentials**, handles verification/2FA and
CAPTCHAs (escalating to a human when blocked), then returns a **reusable session bundle** and an
**auth-flow recording**. Model-agnostic, Go, deterministic-first. Exposed as a **CLI** and a
**job-based MCP server**.

The approved design is `docs/ARCHITECTURE.md`. Build status and how to resume are in
`docs/PROGRESS.md`; autonomous decisions are logged in `docs/DECISIONS.md`.

## Quick start

```sh
# prerequisites: Go 1.27+, Google Chrome / Chromium
go install golang.org/x/tools/cmd/goimports@latest   # for `make lint`
make build

# run the local mock-app lab (offline e2e target)
make lab                                 # serves on 127.0.0.1:8089

# log in to a lab app with provided creds and get a session bundle back
export AGENTBROWSER_KEY=$(head -c 32 /dev/urandom | base64)   # credential-store key
go run ./cmd/agentbrowser creds put --target simple --account alice \
    --username alice@example.com --password 'correct horse'
go run ./cmd/agentbrowser auth --target simple --account alice --mode login \
    --login-url http://127.0.0.1:8089/simple/login
go run ./cmd/agentbrowser export-session --account alice
```

## Layout

See `docs/ARCHITECTURE.md` §10. Short version: `cmd/agentbrowser` (CLI), `cmd/authlab` (lab),
`internal/{server,core,skills,perceive,browser,model,auth,verify,captcha,handback,identity,audit,config,eval,explore}`.

## Testing

```sh
make test     # unit tests (mock LLM, no browser)
make e2e      # browser e2e against the lab (AGENTBROWSER_E2E=1; needs Chrome)
make lint     # gofmt/goimports/vet
```

Real LLM providers are only exercised when their API-key env var is set (e.g.
`OPENAI_API_KEY`, `OPENAI_BASE_URL` for OpenAI-compatible endpoints incl. Ollama/vLLM).
No real third-party sites, paid services or secrets are used in tests.
