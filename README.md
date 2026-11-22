# chromancer

chromancer logs into a website for you with credentials you supply, deals with
2FA and CAPTCHAs on the way in, and hands back the cookies and tokens so your own
code can keep using the session. The command is called `agentbrowser`.

## What it does

- Logs in or signs up with a username and password you provide.
- Fills in 2FA codes: it generates TOTP codes from a seed, or asks you (or your tool) for a code sent by email or SMS.
- Solves common CAPTCHAs automatically and falls back to a human only when it has to.
- Hands back a session bundle: cookies, storage, auth headers and token expiry you can replay without a browser.
- Saves the whole login as a HAR file so you can see what happened.
- Crawls the app once it is logged in — driven by an LLM you can steer with a prompt.
- Runs as a CLI, or as an MCP server for an agent to drive.

## Requirements

Go 1.27 or newer, and Google Chrome or Chromium. If neither browser is
installed, go-rod downloads one on first run.

## Quick start

Build the command, generate a key, and start the test lab in the background:

```sh
go build -o bin/agentbrowser ./cmd/agentbrowser
export AGENTBROWSER_KEY=$(bin/agentbrowser newkey)
make lab &   # mock apps on http://127.0.0.1:8089
```

Store credentials, log in, and print the session:

```sh
bin/agentbrowser creds put --target http://127.0.0.1:8089/simple/ \
  --account alice --username alice@example.com --password 'correct horse'
bin/agentbrowser auth --account alice --login-url http://127.0.0.1:8089/simple/login
bin/agentbrowser export-session --account alice
```

`auth` looks up the target from the stored credentials, so you pass `--target` once, to `creds put`. The password above is the lab's test password.

## Usage

Store credentials for an account (add `--totp-seed` for TOTP 2FA):

```sh
agentbrowser creds put --target URL --account NAME --username U --password P
```

Log in. Use `--mode signup` to register, and `--headful` to watch the browser:

```sh
agentbrowser auth --account NAME --login-url URL                          # agent driver (default)
agentbrowser auth --account NAME --login-url URL --auth-driver deterministic  # deprecated code-first flow
```

**The agent driver is the default.** The LLM agent is the sole driver of the
sign-in: it finds and fills the form, clears interstitials, enters verification
codes, solves CAPTCHAs, and confirms success itself. It needs a configured model
and fails fast without one. The account's real secrets never enter the model —
the agent fills them through tools — and the account-safety deny-list is always
enforced.

`--auth-driver deterministic` selects the older code-first flow (entry finder +
flow handlers + resolve loop, with the agent as recovery). **It is deprecated**
and will be removed in a future release; selecting it prints a warning. Prefer
the agent driver.

Print the saved session, or the full bundle with `--bundle`:

```sh
agentbrowser export-session --account NAME
```

Crawl the app once logged in, or run the MCP server for an agent to drive:

```sh
agentbrowser crawl --account NAME --prompt "Explore billing and the API keys page"
agentbrowser mcp
```

The crawl is LLM-driven: an LLM chooses each action. `--prompt` (or
`--prompt-file`) gives it a focus. `--driver` is **deprecated** (one release) and
ignored. The account-safety deny-list is enforced in
code regardless of any prompt, and the account's real secrets never enter the
model — it types `{{account.password}}`-style placeholders the tool substitutes.

## Configuration

Everything is flags and environment variables; there is no config file.

| Variable | Meaning |
|---|---|
| `AGENTBROWSER_KEY` | Required. base64 of 32 random bytes; encrypts stored credentials and sessions. `agentbrowser newkey` prints one. |
| `AGENTBROWSER_WORKSPACE` | Where accounts and saved files live (default `.workspace`). |
| `AGENTBROWSER_BACKEND` | Browser engine: `chromium` (default) or `camoufox` (anti-detect Firefox, Linux + Xvfb — see [docs/CAMOUFOX.md](docs/CAMOUFOX.md)). Pin per host with `AGENTBROWSER_BACKEND_TARGETS="host=camoufox,…"`. |
| `AGENTBROWSER_HEADFUL` | Set to `1` to show the browser window. |
| `AGENTBROWSER_PROXY` | Upstream proxy URL for the browser. |
| `AGENTBROWSER_CHROME` | Path to a specific Chrome or Chromium binary. |
| `AGENTBROWSER_LLM` | Model provider: `mock` (default), `openai`, `anthropic`, `gemini`. |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` / `GEMINI_API_KEY` | Turn on a real model (with its matching `_MODEL` and optional `_BASE_URL`). |
| `AGENTBROWSER_LLM_VISION` | `1`/`0` to force the model's **vision** capability on/off (default: on for `openai`/`anthropic`/`gemini`). Vision enables **visual mode** — the agent escalates to a screenshot + set-of-marks when the DOM can't be read (closed shadow root, canvas, cross-origin iframe), and acts by mark number or coordinate. |
| `AGENTBROWSER_SOLVER_PLUGIN` | Path to an external CAPTCHA solver plugin. |
| `AGENTBROWSER_CAPTCHA_CONFIG` | Path to a YAML file that sets the solver chain. |

A model is only a fallback for when the built-in rules cannot find the login form, so with no key set everything still works offline against the lab.

## MCP

`agentbrowser mcp` speaks the Model Context Protocol over stdin and stdout, with
these tools:

- `start_auth` and `start_crawl` begin a job and return a job id.
- `get_job` reports progress, or asks for a verification code or a human.
- `resolve_challenge` answers a verification or human-handoff request.
- `cancel_job` stops a job, and `export_session` returns the saved session.

## Development

```sh
make test   # vet and unit tests, with the race detector
make lint   # gofmt, goimports, vet
make lab    # run the mock-app lab
make e2e    # browser tests against the lab (needs Chrome)
```

`agentbrowser eval` benchmarks the login flow against the lab; add `--captcha` to
benchmark the CAPTCHA solver chain.

## Security

A session bundle and the login recording contain live credentials and tokens. They are written only inside the workspace, encrypted at rest, and never written to the audit log.

## Further reading

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): how it works inside.
- [docs/CAPTCHA.md](docs/CAPTCHA.md): CAPTCHA detection and the solver chain.
- [docs/VERIFICATION.md](docs/VERIFICATION.md): where 2FA codes come from.
- [docs/PLUGINS.md](docs/PLUGINS.md): writing a solver plugin or a verification provider.
