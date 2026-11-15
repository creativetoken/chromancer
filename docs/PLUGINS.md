# Plugins and extension points

How to plug your own CAPTCHA solver or verification source into chromancer, and
what to know about the files it writes.

## Writing a CAPTCHA solver plugin

A solver only solves. It receives a challenge and returns a result; it never
touches the browser (the core applies the result through a per-vendor injector).
Solvers run out of process and speak a line-delimited JSON protocol over stdin
and stdout.

Implement `captcha.Solver` and serve it with `plugin.Serve`:

```go
package main

import (
    "context"
    "os"

    "github.com/creativetoken/chromancer/internal/captcha"
    "github.com/creativetoken/chromancer/internal/captcha/plugin"
)

type mySolver struct{}

func (mySolver) Name() string { return "my-solver" }

func (mySolver) Describe() captcha.Capabilities {
    return captcha.Capabilities{
        Vendors: []captcha.Vendor{captcha.VendorRecaptchaV2},
        Kinds:   []captcha.Kind{captcha.KindInteractive},
    }
}

func (mySolver) Solve(ctx context.Context, c captcha.Challenge) (captcha.Result, error) {
    return captcha.Result{Kind: captcha.ResultToken, Token: "..."}, nil
}

func (mySolver) Report(ctx context.Context, solveID string, accepted bool) error { return nil }

func main() { _ = plugin.Serve(mySolver{}, os.Stdin, os.Stdout) }
```

Build it and point `AGENTBROWSER_SOLVER_PLUGIN` at the binary, or add it to the
`external` list in a captcha config file (`AGENTBROWSER_CAPTCHA_CONFIG`). The
wire schema is `internal/captcha/plugin/proto/solver.proto`, and the Go
transport matches it (see `docs/DECISIONS.md` for why the transport is
stdio-JSON rather than gRPC). Every plugin must pass `plugin.Conform`.

`plugins/captcha-generic/` is a full example: an adapter for the common
token-solving service API (create a task, poll for the result, report whether it
worked). See `docs/CAPTCHA.md` for the chain, config and how to add a new vendor.

## Writing a verification provider

A verification provider answers a request for a 2FA code, link or approval. The
agent detects the channel and asks; your provider returns the value.

```go
type Provider interface {
    Supports(ch verify.Channel, account string) bool
    GetCode(ctx context.Context, req verify.Request) (verify.Result, error) // Code | Link | Approved
    Resend(ctx context.Context, req verify.Request) error
}
```

Built-in providers are `TOTP` (from a seed), `CLI` (prompts the terminal),
`Webhook`, and `Ask` (over MCP). `verify.Chain` tries providers in order by
channel. See `docs/VERIFICATION.md`.

## Files it writes

The session bundle (cookies, storage, observed `Authorization` and CSRF headers,
token expiry) and the login recording (HAR-style request and response of the
login, plus a short window after it, with bodies kept and secrets flagged)
contain live credentials and tokens. They are:

- returned only to the caller, as file paths or MCP resource URIs, never inline;
- written only inside the per-account workspace, with `0600` permissions;
- redacted before every audit-log write, so the audit log never holds a
  plaintext password or token.

Credentials and saved sessions are encrypted at rest with NaCl secretbox, using
the key from `AGENTBROWSER_KEY`. A bundle's `replayable` flag says whether a
plain HTTP replay works, or a browser is still needed (for example when the app
computes per-request headers in JavaScript, or binds a token to the IP).
