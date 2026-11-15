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
