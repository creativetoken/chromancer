# DECISIONS

> One line per decision made autonomously: date · decision · why · alternatives considered.
> Design-level changes also update the affected section of `docs/ARCHITECTURE.md`.

- 2026-11-15 · **Module path `github.com/creativetoken/chromancer`** (repo provided by operator
  mid-build; initially `agentbrowser`) · matches the real remote so `go get` works · binaries keep
  the spec'd names `agentbrowser` / `authlab`.
- 2026-11-15 · **Lab app code lives in `internal/lab` (not `testdata/lab/`)** · Go tooling ignores
  `testdata/` directories, so the lab binary can't be built from there; `testdata/lab/` holds HTML
  fixtures only · alt: a separate module under testdata (awkward).
- 2026-11-15 · **v1 persistence is file-based, not SQLite**: JSONL hash-chained audit log, secretbox-
  encrypted creds/session files, artefact files in the per-account workspace · the v1 data set is tiny
  and artefacts are by-reference anyway; avoids a heavy pure-Go SQLite compile · alt: modernc sqlite
  (ADR 0006 — revisit if Phase 2 needs relational queries).
- 2026-11-15 · **Perception via an injected DOM walk computing accessible names** (aria-label →
  labelledby → label[for] → placeholder → text), tagging nodes with `data-ab-handle`, run in the main
  frame and each iframe · handles must map back to actable elements across frames, which CDP
  `Accessibility.getFullAXTree` doesn't give directly · alt: CDP AX tree + backend-node lookup (more
  moving parts).
- 2026-11-15 · **Proto generation via go-installed `buf` + `protoc-gen-go`/`protoc-gen-go-grpc`**,
  no system `protoc` · `protoc` isn't installed and `buf` compiles protos itself · alt: brew
  install protobuf (system dependency), hand-maintained `.pb.go` (fragile).
- 2026-11-15 · **Lab apps served on one port under path prefixes** (`/simple/…`, `/modal/…`) ·
  one process, one port, simple to start/stop in tests; cookies are scoped by path to isolate
  apps · alt: one port per app (more ports to manage in CI).
- 2026-11-15 · **Credential-store encryption = NaCl secretbox (x/crypto) with a 32-byte key from
  `AGENTBROWSER_KEY` (base64)** · minimal dependency, authenticated encryption · alt: `age`
  (heavier), OS keyring (not CI-friendly).
- 2026-11-15 · **Persistence = SQLite via `modernc.org/sqlite` (pure Go, no CGo)** per ADR 0006 ·
  cross-compiles, no toolchain needs · alt: `mattn/go-sqlite3` (CGo).
- 2026-11-15 · **Chrome binary resolution**: use the installed Google Chrome if found, else let
  go-rod's launcher download a browser · avoids network in the common case · alt: always download.
- 2026-11-15 · **Deterministic "skills" are realized in `auth` (FlowHandlers: form/multistep/
  signup, success detection, verification) and `explore` (nav/form/pagination), not a separate
  `internal/skills` package** · the skill set the spec anticipated is small and cohesive with its
  owners; a standalone registry would be indirection without payoff · ARCHITECTURE §10 lists
  `skills/`; the empty dir was removed. Revisit if a cross-flow skill library emerges.
- 2026-11-15 · **GitHub push deferred — remote returns 404** · operator asked to push to
  `github.com/creativetoken/chromancer.git`, but the repo does not exist yet and there is no `gh`
  CLI to create it; `git push` cannot create a repo · committing locally and retrying the push each
  milestone; once the repo is created the existing history pushes cleanly · not a hard stop.
- 2026-11-15 · **Post-login capture does a `Reload`** after optional navigate · guarantees
  authenticated XHRs (bearer, /me, per-request-signed calls) re-fire into the post-login window even
  on SPAs whose landing is a hash-route change that Navigate won't reload · alt: synthesize an XHR
  (brittle, app-specific).
- 2026-11-15 · **`Classify` prefers a real `type=submit` over a text-matched button** · a header
  "Sign in" button (modal opener) was being chosen as the form submit · alt: positional heuristic
  (submit after password) — kept as the secondary fallback.
- 2026-11-15 · **External CAPTCHA solver transport = subprocess stdio-JSON (go-plugin-style
  handshake), not gRPC** · the env has no `protoc`/`buf`, and pulling grpc+protobuf+go-plugin +
  a codegen toolchain into dev/CI violates "minimal, justified deps"; the stdio-JSON transport is
  still out-of-process (real isolation), language-agnostic, and fully testable offline · the
  normative message schema is shipped as `internal/captcha/plugin/proto/solver.proto` (schema of
  record) and the Go transport mirrors it exactly, so a gRPC transport can be added later behind
  the same `Solver` contract without touching callers. ARCHITECTURE §5/§10 and ADR 0011 say
  "go-plugin/gRPC"; this is the smallest change that preserves the contract and acceptance
  (out-of-process mock solver + conformance suite) given the toolchain constraint.
- 2026-11-15 · **Lab inbox endpoint `GET /__lab/inbox?to=<addr>`** returns delivered messages as
  JSON; the `labinbox` VerificationProvider polls it · simplest machine-readable inbox · alt: a
  fake SMTP server (unnecessary).

### CAPTCHA solving module (C1–C5)
- 2026-11-15 · **CAPTCHA detection judged by computed visibility, not script/iframe presence** ·
  invisible/Enterprise reCAPTCHA, invisible hCaptcha and managed Turnstile all load their script
  and inject hidden iframes (badge is visibly *sized*; bframe present but `visibility:hidden`) with
  no challenge to solve; matching presence dead-ended real logins (Freshworks) into a human handoff
  before submit · the browser probe uses computed `display/visibility/opacity`; interactive fires
  only on a shown widget or an actually-visible challenge popup · alt: bounding-box size (lies for
  invisible widgets).
- 2026-11-15 · **One unified detector + a pure classifier shared by a browser JS probe and a
  pure-Go `x/net/html` probe** · the same classification must run on the live DOM, on saved HTML
  fixtures (fast unit tests, no browser), and on a failed-submit response body · added
  `golang.org/x/net` as a direct dep for fixture/response-body parsing (pinned v0.62.0 to keep
  `x/crypto` at v0.59.0) · alt: browser-only detection (can't unit-test fixtures) or regex HTML
  scraping (fragile). Replaces the per-vendor `signatureDetector`/`MockDetector`; `DetectAny` /
  `DefaultDetectors` surface unchanged.
- 2026-11-15 · **Score/invisible CAPTCHAs are non-blocking in the flow** · reCAPTCHA v3/Enterprise,
  invisible v2 and invisible Turnstile execute on submit and attach their own token — the flow
  submits and lets the page resolve them; never a human handoff for a score challenge · alt: treat
  any detected captcha as blocking (false dead-ends).
- 2026-11-15 · **captchaHook advances through the chain on a decline; human handoff only on
  exhaustion** · the pre-submit hook used to hand off on the first solver's decline, so
  testmode→native→vision fallback never happened within one submit · it now loops eligible solvers
  (decline → next) and `native` declines when its click leaves the response field empty; budget
  errors propagate so exhaustion-by-budget surfaces as `failed{budget_exhausted}`.
- 2026-11-15 · **Acceptance is decided by the challenge being gone; the reached-backend token check
  is corroborating, not a veto** · a top-level form-POST body is not always captured by the recorder
  (only XHR/fetch reliably is), and a token that never reached the backend leaves the challenge
  standing anyway, so challenge-gone is the sound signal · outcome is settled on *authenticated*
  (accepted) or *challenge re-detected* (rejected), not on a mid-navigation snapshot (which raced the
  redirect and under-counted solver success).
- 2026-11-15 · **testmode answers the vendors' published TEST sitekeys** (reCAPTCHA v2/hCaptcha/
  Turnstile) with their documented always-pass tokens, from the vendors' public docs; real keys are
  declined · lets the offline lab exercise the real detect→inject→submit path without defeating
  anything real · alt: a bespoke lab-only token (doesn't exercise vendor response-field contracts).
- 2026-11-15 · **External solver transport stays stdio-JSON (ADR 0011); the `plugins/captcha-generic`
  adapter speaks the common create-task→poll→report HTTP service API** · matches real token-solving
  services; configured by env; tested against a local httptest mock (never a real service).
- 2026-11-15 · **Lab CAPTCHA mocks never load real vendor CDN scripts** · the e2e must be offline and
  deterministic; the real Turnstile/reCAPTCHA scripts fetch the internet and render live widgets that
  broke the flow · detection keys off the DOM widget + data attributes (reCAPTCHA v3 needs a
  `render=` src, served from a local lab stub path).
- 2026-11-15 · **Added `golang.org/x/net` (HTML fixtures) and `gopkg.in/yaml.v3` (chain config)** ·
  pure-Go fixture parsing for fast detector unit tests, and the operator-facing YAML chain config;
  both pinned to keep `x/crypto` at v0.59.0 · alt: browser-only detection tests (slow) and JSON-only
  config (spec asks for YAML).

### Proposals (not yet done)
- 2026-11-15 · **PROPOSAL: align the names** · the repo/module is `chromancer` but the binary,
  env vars and MCP server are `agentbrowser`. Two names for one thing is a small ongoing
  papercut (docs, support, muscle memory). Option A: rename the binary to `chromancer` and the
  env prefix to `CHROMANCER_` (breaking for anyone with scripts/`AGENTBROWSER_KEY` set). Option
  B: rename the module to `agentbrowser` (breaks import paths). Not done in this task; the README
  states the relationship in one line. Decide before a wider release.

### Verification module (V1–V5)
- 2026-11-15 · **Twilio SMS via plain REST, not the twilio-go SDK** · the one call needed (list
  inbound Messages) is a single authenticated GET; pulling the SDK + its deps in for that is not
  justified · the REST client is ~1 file, fully testable against an httptest mock, and keeps the
  dependency set small.
- 2026-11-15 · **Date filtering is client-side, not a server `DateSent>` query param** · Twilio's
  date-range query encoding is fiddly and easy to get subtly wrong; fetching the recent page and
  filtering by parsed `date_sent >= since - skew` is simpler and robust · alt: server-side range
  (brittle across API/SDK versions).

- 2026-11-15 · **Email provider lives in `internal/verify` as a flat file (`imap.go`), not a
  sub-package** · every existing verification provider (TOTP, CLI, Webhook, Ask, LabInbox) is a
  flat file implementing `verify.Provider` in that package; a `verify/imap` sub-package would be
  inconsistent and force exported-type churn · modelled on the reference at
  `ai-hackbot/internal/mailbox/mailbox.go`, adopting its robustness patterns (persistent
  connection with a NOOP health check + redial, junk/spam folder auto-discovery via the
  SPECIAL-USE extension with a common-name fallback, bounded retry with backoff).
- 2026-11-15 · **Kept go-imap v1 (not the reference's v2)** · v1 ships an in-memory backend +
  server used by our offline tests (alias separation, Spam, since, claim-once, reconnect); v2 has
  no ready in-memory backend, so moving to it would mean rewriting those tests against a hand-rolled
  server for no behavioural gain · the reference's lessons are transport-agnostic and applied on v1.

### LLM-driven crawl (driver: bfs | llm | hybrid)
- 2026-11-16 · **One-action-per-step via `model.LLMProvider.Structured`, not a new tool-calling
  layer** · `Structured(msgs, schema)` already negotiates capabilities (native tool calling →
  JSON schema → generate/validate/repair), which is exactly the spec's "tool calling with a
  JSON-schema fallback" · building a separate tool-call path would duplicate that ladder.
- 2026-11-16 · **The deny-list is enforced in a shared `explore.Executor`, never in the prompt** ·
  page text and the operator prompt are untrusted; defence lives in code · the Executor checks
  `DenyReason` before every click/submit/navigate and returns `blocked:<category>` to the model.
  The BFS driver keeps its existing boundary checks; both share `DenyReason`.
- 2026-11-16 · **Secrets never enter the model context; the model types placeholders** ·
  `{{account.password|email|phone}}` and `{{totp}}` are substituted by the Executor at type time,
  the audit records the placeholder, and values typed into email/phone fields that are not the
  account's own placeholders are flagged in the trace.
- 2026-11-16 · **Default driver stays `bfs`** · the LLM driver costs tokens and is opt-in via
  `--driver llm|hybrid` or by passing `--prompt`/`--prompt-file` · hybrid uses BFS for breadth and
  the LLM for forms/ambiguous controls/frontier choice.
- 2026-11-16 · **System prompt in a versioned template `prompts/crawl_system.md`** · overridable
  with `--system-prompt-file`; the operator prompt is inserted into a clearly delimited
  "Operator instructions" section and cannot disable the deny-list.

### Iterative LLM entrypoint discovery
- 2026-11-16 · **The LLM entry fallback navigates across steps instead of a single
  pick** · on content sites (e.g. BuzzFeed) the login/signup control is hidden behind an account
  or hamburger menu, or on an auth subdomain, so the one-shot "pick a node on the root" fallback
  found nothing · `tryLLMIterative` loops perceive→ask→click→re-check up to 6 steps (opening a menu,
  then clicking the revealed Sign in/up control), stopping on success, a give-up (handle 0), no
  progress (same signature twice), or the step cap. Lab app `menu-signup` hides its signup at a
  non-guessable path behind an account menu so only this path can reach it. `App.AuthModel` is a
  test seam to script the classify model for entry discovery.
- 2026-11-16 · **Restored-session validation does not trust injected cookies/storage** · the
  saved session is imported into a blank page, so the restore check runs against an empty baseline
  where every cookie looks "new"; infrastructure cookies (Freshservice `_itildesk_session`,
  Cloudflare `__cf_bm`, reCAPTCHA `_GRECAPTCHA`) then masqueraded as `new_auth_cookie` and a
  logged-out bot-challenge page was reported "restored healthy session, no login" with a
  non-replayable bundle · `SuccessDetector.IsAuthenticated` gained a `trustStorage` arg: false on
  restore (require a server-confirmed signal — /me 2xx or an authed-only element — else fall through
  to a real login), true on live login (server mints the cookies during the flow). Cookie/storage
  scoring extracted to the pure `storageEvidence` helper and unit-tested with the Freshservice set.
- 2026-11-16 · **The LLM provider is built vision-capable by default** · `buildRouter` constructed
  the Anthropic/OpenAI/Gemini provider without the `Vision` flag, so `Capabilities().Vision` was
  false; the vision CAPTCHA solver then declined every real image challenge ("no vision-capable model
  configured") and reCAPTCHA v2 image grids always fell through to human handoff · added
  `LLMConfig.Vision`, defaulted true for the hosted providers (Claude 3+/GPT-4o+/Gemini 1.5+ all read
  images) and false for mock, overridable via `AGENTBROWSER_LLM_VISION`. Note: enabling vision lets
  the solver *engage*; a v2 image grid (cross-origin iframe, multi-round) is still a hard target —
  the external-plugin slot or not triggering it remain the reliable paths.
- 2026-11-16 · **Headless Chrome no longer announces itself in the UA / client hints** · Chromium
  reports `HeadlessChrome` in both `navigator.userAgent` and the `Sec-CH-UA` brand list — an instant
  bot tell that pushes reCAPTCHA/Cloudflare to the hardest challenges · at launch the browser now
  reads the real UA + high-entropy `navigator.userAgentData` and re-applies them on every page via
  `Network.setUserAgentOverride` with the `HeadlessChrome` marker rewritten to `Chrome` /
  `Google Chrome`, so the UA string, Sec-CH-UA headers and `navigator.userAgentData` all stay
  consistent with the real build/platform and never leak headless. `AGENTBROWSER_UA` pins an explicit
  UA string. `navigator.webdriver` was already hidden via `disable-blink-features=AutomationControlled`.
  Deriving from the live browser avoids version drift (no hardcoded Chrome version to go stale).
- 2026-11-16 · **Form handlers re-perceive after the CAPTCHA hook before submitting** · the submit
  control was located from the perception taken *before* `beforeSubmit`; when the hook hands off to a
  human who solves a challenge in the browser, the solve callback re-renders the form, so the
  pre-hook handle is detached and clicking it fails with `submit: element "[data-ab-handle=N]":
  context deadline exceeded` (seen live on Freshservice) · a shared `actor.submitAfterCaptcha`
  (used by Form/MultiStep/Signup handlers) runs the hook, then re-perceives and submits the fresh
  control — or returns if the form already advanced (the solve auto-submitted), letting the resolve
  loop detect success/verification. Lab app `captcha-rerender` reproduces it (polls solve status and
  swaps the submit button on solve); its e2e fails on the stale handle without the fix and logs in
  with it.
- 2026-11-17 · **Verification code steps are detected even when email/password stay on the form,
  language-independently** · some sites (Bambu Lab's `/sign-in`) keep the email and password inputs
  visible and reveal a one-time-code field in the *same* form for step-up verification; the live
  login filled credentials, submitted, then looped re-submitting the login form until `stuck: form
  did not progress`, because `DetectVerifyStep` only treated a page as a verify step when the
  password field was gone, and its code-copy trigger was English-only · the detector now scores a
  code field from structural, language-neutral signals — `autocomplete=one-time-code`,
  `inputmode=numeric`, a numeric/tel type, a short `maxlength` (4–8), and a position below the
  credential fields — picks the strongest candidate that is neither the identifier nor the password,
  and fires when that score is strong (`otpStrong`) OR on-page copy asks for a code; it also prefers
  a Confirm/Verify-style button over a lingering "Log in" for submitting the code. `perceive.Node`
  gained `InputMode`/`MaxLength` (type=password is excluded from code candidates so a "confirm
  password" signup field is never mistaken for one). Lab apps `combined-verify` (English copy) and
  `combined-verify-intl` (non-English labels, code field detectable only from inputmode+maxlength)
  reproduce it end-to-end; both log in via the emailed code. Verified live against bambulab.com.
- 2026-11-17 · **Relay/sub-address aliases: canonicalise to resolve the inbox, exact-match to claim
  the code** · an OTP delivered to a `user+N@relay.example` address that forwards into a Gmail inbox
  must be fetchable, while two accounts sharing one relay base (`+1` vs `+5`) must never read each
  other's codes · mailbox *resolution* (`MailboxForAddress`) already canonicalises the `+N` tag, so
  listing one relay alias in a mailbox's `address_envs` (new `GMAIL_ALIAS_3` slot in the example
  resources) matches every `+N` on that base; per-account message *matching* (`parsedMsg.toAlias`)
  keeps using the full address including `+N`, so interleaved OTPs stay separated. Guarded by
  `TestIMAPSubAddressSeparation` (two `+N` accounts, interleaved mail, each gets only its own newest
  code). Real relay/inbox addresses live only in the operator's git-ignored `.env.live`; configs and
  tests use `example.com` placeholders.
- 2026-11-17 · **Agent takeover is a new escalation step between the deterministic flow and human
  handoff** (`deterministic → agent → human → failed{reason}`) · a login the deterministic flow
  cannot finish (stuck form, unrecognised interstitial, verification timeout) was previously lost to
  a human handoff or a hard failure even when an LLM could have clicked through; the agent gets the
  SAME live page and tries, and only its failure falls through to a human · wired at the single
  `recoverDeadEnd` chokepoint in `auth.resolve` (the former `escalate` closure), which both existing
  dead ends and now the form-error-exhausted / verify-timeout / loop-exhausted paths route through.
- 2026-11-17 · **The takeover reuses the existing `explore.Explorer{Driver: DriverLLM}` driver
  rather than a second agent loop** · the crawl driver already owns the observation builder, action
  schema, the `Executor` that enforces the account-safety deny-list + secret-placeholder
  substitution (real secrets never enter the model) + untrusted-DATA fencing, loop/step/plateau
  budgets, audit, and a scriptable mock; `auth` may import `explore` (no cycle) · the takeover adds
  five tools to that driver — `enter_verification` / `resend_verification` / `solve_captcha` /
  `check_logged_in` / `request_human` (+ `give_up`) — advertised only when their callback is wired,
  so the ordinary crawl schema is unchanged; the callbacks delegate to the Authenticator's own
  `doVerify` / `captchaHook` / `SuccessDetector` / `doHandoff`. A fetched OTP/link is returned to the
  model only as an outcome label, never as the value · alt: a bespoke takeover agent (would
  duplicate the deny-list and secret handling — the exact code that must not diverge).
- 2026-11-17 · **Takeover success is gated ONLY by `check_logged_in` (deterministic success
  detection); the model saying "done" is never enough** · and a trigger matrix decides agent vs
  human vs hard-fail: bot-wall / block pages (`IsBlockPage`) and an exhausted captcha chain go
  STRAIGHT to a human (the agent must never retry into them); `budget_exhausted`, `cancelled`, and a
  confirmed bad-credentials wall (`isCredentialsRejection`, → new `ErrCredentialsRejected`) are never
  recovered. CLI `--recovery agent|human|off` (default `agent`, which degrades to human-only when no
  LLM provider is configured); MCP `get_job` gains a `recovering{step,reason}` state. Nine lab apps +
  mock-LLM e2e tests prove each solvable scenario completes, the bot-wall goes to a human with zero
  agent calls, a wrong password fails without the agent, a prompt-injection "delete account" is
  blocked by the deny-list while the legit login still completes, and no secret reaches the model.
- 2026-11-20 · **Backend-neutral browser layer via interfaces + a registry; go-rod isolated to
  `internal/browser/rodbackend`** · a second engine (Camoufox) needs the browser API free of
  go-rod types. `internal/browser` keeps the neutral value types (`Cookie`, `SessionState`,
  `RequestEvent`/`ResponseEvent`, `NetworkSink`, `Options`) and defines interfaces `Browser` /
  `Page` / `Frame` / `Element` + a neutral `Value` (only `Str`/`Bool`/`Int`/`Float`/`Nil`/
  `Unmarshal` are used by callers). `Frame` exposes `Eval` (perceive runs per-frame JS); `Element`
  is an opaque handle (callers only pass it back to `Page` methods). Backends self-register via
  `browser.Register(name, Factory)`; callers use `browser.Open(ctx, name, Options)`. This keeps
  `internal/browser` import-free of any engine, so `git grep go-rod/rod` hits only `rodbackend`.
  The `Rod()` escape hatches are dropped (no engine type in the exported API). · alt: a build-tag
  swap (can't run both engines in one binary / pick per target) or generics over the rod types
  (still leaks rod in signatures).
- 2026-11-20 · **Phase B (Camoufox) is partially deferred on this host; Phase A ships in full** ·
  host is macOS (Darwin arm64) with no `Xvfb`/`x11vnc` (Linux-only); Camoufox `headless="virtual"`
  and the x11vnc/noVNC handoff view cannot run natively here, and the B0 live spike (compare
  chromium vs camoufox on real bot-detection sites) was left with unfilled site/host placeholders in
  the brief — running it would cross the "no real-site interaction beyond the authorized spike" hard
  stop with undefined targets. Decision: implement and verify Phase A completely on chromium; write
  the Phase B pieces that build/unit-test here (sidecar launcher + `scripts/camoufox-setup.sh` +
  `Dockerfile.camoufox`, the playwright-go client behind the interface with the version-match check,
  the token-guarded handoff proxy + its refusal test); defer the live-only parts (B0 real-site
  spike, Xvfb-backed e2e, live VNC view) under PROGRESS.md "Deferred" with this reason. · alt: run
  everything in a Linux Docker container — deferred because the live site comparison still needs
  defined targets and a controlled IP the brief did not supply.
- 2026-11-20 · **Camoufox backend uses `github.com/playwright-community/playwright-go` v0.5200.0
  (Playwright 1.52.0), pinned to `camoufox.PlaywrightVersion`** · the brief named
  `mxschmitt/playwright-go`, which now redirects to the `playwright-community` fork (same versions);
  Go resolves the canonical module path, so we require the canonical one. The module version
  `v0.MMmm.patch` encodes the Playwright version; `CheckPlaywrightVersion` fails a major/minor skew at
  sidecar startup. · alt: the mxschmitt path (stale redirect).
- 2026-11-20 · **Two engine differences bridged inside the camoufox backend so shared code is
  untouched:** (1) Playwright's `Evaluate` passes a SINGLE arg to the JS function (rod passes many),
  so when a caller supplies >1 arg the backend bundles them into one array and wraps the expression
  as `(__a) => (EXPR)(...__a)` — the shared perceiver `walkJS` runs unchanged on both. (2) Playwright
  resource types are lowercase (`document`,`xhr`,…); the backend maps them to the CDP-style
  capitalised `Type` (`Document`,`XHR`,…) the `AuthRecorder` already keys on, so capture + the
  replayability check are backend-agnostic. · alt: change the shared perceiver/recorder per backend
  (would fork the exact code that must not diverge).
- 2026-11-20 · **Camoufox has no persistent profile: session state is cookies (context
  `AddCookies`) + a per-origin init script that seeds local/sessionStorage BEFORE page scripts run**
  (playwright `AddInitScript`), restored into a fresh context each run. UA is never overridden
  (`SupportsUAOverride=false`) to keep Camoufox's fingerprint consistent. · alt: Playwright
  `storage_state` on context creation — the init-script approach also covers sessionStorage (which
  storage_state does not) and matches addition #4's "before page scripts" requirement.
- 2026-11-20 · **Popups/new-auth-windows (addition #7) deferred behind the `browser.Page`
  interface** · the interface does not surface popups (`OnPopup`) and no lab app / login flow in the
  suite opens one, so adding the method + a lab e2e would be unverifiable here. Recorded as Deferred
  in PROGRESS with the reason; revisit when a real target needs an auth popup. · alt: add a
  speculative `OnPopup` now — rejected as untestable dead surface.
