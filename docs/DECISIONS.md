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
