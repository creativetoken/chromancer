# Verification (2FA codes and links)

When a login asks for a second factor, the agent works out the channel (email,
SMS, TOTP or push) and a hint ("code sent to \*\*\*1234"), then asks a provider
for the value: a one-time code to type, a magic link to open, or an approval
that was granted out of band. It can fetch these itself from the operator's real
Twilio numbers and Gmail mailbox, generate TOTP codes locally, or fall back to
asking you (CLI) or your tool (MCP).

In agent-only mode (ADR 0017) the LLM agent invokes verification through two
tools — `enter_verification(channel, want)` fetches the current code or magic
link from the account's own channel and enters it, and `resend_verification`
requests a fresh one — so the agent never sees or types the code itself: it only
chooses the channel and when to act. The provider chain below backs those tools.

## Resources config

Describe the operator's numbers, mailboxes and accounts in a YAML file and point
`AGENTBROWSER_RESOURCES` at it. Secrets are referenced by environment-variable
name only; put no real numbers, addresses, tokens or passwords in the file. See
`configs/resources.example.yaml`. The file is validated at startup: every
account email maps to exactly one mailbox, every phone exists, and every
referenced environment variable is set (it fails fast and names any that are
missing, never printing a value).

## Email (Gmail over IMAP)

Gmail no longer accepts your normal password over IMAP. Pick one auth mode per
mailbox with `auth:`.

### App password (default for Gmail)

The simplest mode, and the Gmail default. It needs **2-Step Verification on**.

1. Turn on 2-Step Verification for the Google account if it isn't already.
2. Create an app password at https://myaccount.google.com/apppasswords.
3. Set `GMAIL_USER` to the mailbox address and `GMAIL_IMAP_PASSWORD` to the app
   password. Google displays it as `abcd efgh ijkl mnop`; the spaces are
   cosmetic and are stripped automatically, so either form works.

Config:

```yaml
- id: gmail-main
  host: imap.gmail.com
  auth: app_password        # the Gmail default; can be omitted
  username_env: GMAIL_USER
  password_env: GMAIL_IMAP_PASSWORD
```

That's it — no Google Cloud project and no `mailbox login` step. IMAP signs in
with `LOGIN` using the app password, which is never logged.

### OAuth2 (when app passwords are disabled)

If a Workspace admin has disabled app passwords, use OAuth2 instead. One-time
Google Cloud setup:

1. Create or pick a project at https://console.cloud.google.com/.
2. Enable the Gmail API (APIs & Services → Library → Gmail API → Enable).
3. Configure the OAuth consent screen (External). Keep it in Testing mode and
   add your Gmail address under Test users. In testing mode no verification is
   needed and refresh tokens last long enough for this use.
4. Create credentials → OAuth client ID → Application type **Desktop app**. Note
   the client ID and secret.
5. Put them in the environment as `GMAIL_OAUTH_CLIENT_ID` and
   `GMAIL_OAUTH_CLIENT_SECRET`, and set `GMAIL_USER` to the mailbox address. Set
   `auth: oauth2` on the mailbox.

Then authorize once per mailbox:

```sh
agentbrowser mailbox login --mailbox gmail-main
```

This runs the installed-app flow: it prints a URL, you open it and grant access,
and Google redirects to a loopback server on 127.0.0.1 (PKCE, scope
`https://mail.google.com/`). The refresh token is stored encrypted in the
workspace and is never printed or logged. Access tokens are refreshed
automatically; if the refresh token is revoked you get a clear message to rerun
`mailbox login`.

### Plain password

`auth: password` (plain LOGIN) is only for non-Gmail IMAP servers. The config
refuses it for `imap.gmail.com`.

### Aliases

One mailbox can receive several alias addresses (plus-addressing or dot
variants). You can list specific aliases under the mailbox (`address_envs`) and
give each account its own alias (`email_env`), but this is optional: any
`+tag` sub-address of the mailbox account resolves to it automatically (Gmail
also ignores dots and treats googlemail.com as gmail.com), so a brand-new signup
alias such as `you+42@gmail.com` works without being pre-listed — it just needs
to be the account's login email. A single connection is shared across the
accounts that alias into the mailbox, and each account only ever receives mail
addressed to its own alias (matched on To / Delivered-To / X-Original-To). Both
INBOX and the spam/junk folder are searched, since verification mail often lands
in spam.

## SMS (Twilio)

Set `TWILIO_ACCOUNT_SID` and `TWILIO_AUTH_TOKEN`, and give each phone a
`number_env`. By default the provider polls the Twilio REST API for inbound
messages to the number, newer than the request time (minus a small clock-skew
allowance), and extracts the code. It prefers a message whose sender or body
matches the app, skips messages a previous verification already used, and asks
the flow to click "resend" once near the midpoint if nothing has arrived.

An optional inbound webhook (off by default, needs a public URL) validates
Twilio's `X-Twilio-Signature` and queues messages instead of polling.

If a page rejects the number ("VoIP numbers not supported", "invalid number"),
the job fails with `phone_rejected`.

## TOTP (authenticator app)

Store a seed per account and target, as a raw base32 secret or a full
`otpauth://totp/...` URI (issuer, algorithm SHA1/256/512, digits 6/8, period
30/60 are all honoured):

```sh
agentbrowser creds put --target URL --account NAME --username U --password P \
  --totp-seed JBSWY3DPEHPK3PXP
```

Codes are computed locally (RFC 6238). If fewer than ~3 seconds remain in the
current window the agent waits for the next one so the code does not expire
before submission; if a code is rejected it retries once with the next window's
code and then stops rather than brute-forcing. When an app offers a setup QR
plus a manual key, the key is read (or the QR decoded) and saved encrypted so
the agent can log back in later; recovery codes shown during setup are saved
alongside. Seeds and recovery codes are never logged, traced or put in a
session bundle.

Check a channel quickly:

```sh
agentbrowser verify test --account acct-a --channel sms --quiet
```

`--quiet` prints only pass/fail and timing, never the value.

## Fallbacks

If an automatic provider times out, verification falls back to asking you: over
MCP the job reports `needs_verification` and waits for `resolve_challenge`, or on
a terminal it prompts you to type the code.

## Troubleshooting

- Codes in spam: the IMAP provider searches the junk folder too; nothing extra
  is needed.
- VoIP numbers rejected: some sites refuse non-mobile numbers; use a mobile
  Twilio number.
- Clock skew: TOTP depends on the system clock; a large drift causes rejected
  codes. Keep the host clock synced.
- `invalid_grant`: the Gmail refresh token was revoked or expired; rerun
  `agentbrowser mailbox login`.

## Privacy

The Gmail mailbox is the operator's real inbox. The agent only runs the narrow
search above, never indexes or stores unrelated mail, keeps only message IDs for
claiming and the one extracted value for the current job, and is read-only by
default (no sending, deleting, moving, labelling or marking as read). An LLM sees
at most one candidate message at a time, and only when the deterministic
extraction is unsure. Codes, links, tokens, passwords, TOTP seeds and recovery
codes are redacted in the audit log and trace.
