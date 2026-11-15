# Verification (2FA)

When a login asks for a second factor, chromancer works out the channel (email,
SMS, TOTP or push) and a hint like "code sent to \*\*\*1234", then asks a
verification provider for the value. The provider returns one of three things: a
one-time code to type, a magic link to open, or an out-of-band approval that has
already been granted.

chromancer does not read your email or SMS inbox for you. It knows a code is
needed and where it was sent; you (or a tool you connect) supply the actual code.

## Built-in providers

chromancer tries these in order, by channel:

- TOTP: generates the code locally from a seed you stored with `creds put
  --totp-seed`. This is the only channel it can answer on its own.
- Ask: when run as an MCP server, the job reports `needs_verification` and waits
  for the caller to send the code with `resolve_challenge`.
- CLI: when run from a terminal, it prompts you to type the code.
- Webhook: fetches the code from an HTTP endpoint you run.

The lab uses a fifth provider, a test inbox, so the email, SMS, TOTP and
magic-link scenarios run offline.

## Using TOTP

Store the base32 seed with the account, and TOTP logins then complete without
any prompt:

```sh
agentbrowser creds put --target URL --account NAME \
  --username U --password P --totp-seed JBSWY3DPEHPK3PXP
```

## Writing your own provider

Implement `verify.Provider` and add it to the chain. See
[docs/PLUGINS.md](PLUGINS.md) for the interface.

Over MCP the flow is asynchronous: `get_job` returns `needs_verification` with
the channel, hint and an expiry; you reply with `resolve_challenge`. The same
`resolve_challenge` call also answers a human-handoff request, so one verb covers
both.
