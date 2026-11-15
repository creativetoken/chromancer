# DECISIONS

> One line per decision made autonomously: date · decision · why · alternatives considered.
> Design-level changes also update the affected section of `docs/ARCHITECTURE.md`.

- 2026-11-15 · **Module path `agentbrowser`** (no host prefix) · private, unpublished module; keeps
  imports short · alt: a guessed GitHub path (wrong handle risk), `example.com/…` (misleading).
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
- 2026-11-15 · **Lab inbox endpoint `GET /__lab/inbox?to=<addr>`** returns delivered messages as
  JSON; the `labinbox` VerificationProvider polls it · simplest machine-readable inbox · alt: a
  fake SMTP server (unnecessary).
