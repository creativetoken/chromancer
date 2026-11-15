# PROGRESS

> Single source of truth for resuming. On restart: read this file and `DECISIONS.md` first, then
> continue from **Next** without redoing finished work.

## Current milestone
**Step 0 — Setup** (in progress)

## Done
- Approved design docs committed (`docs/ARCHITECTURE.md` is the spec).
- Environment verified: Go 1.27.3 (darwin/arm64), Google Chrome present, network OK.
- `go mod init agentbrowser`; `github.com/go-rod/rod` added; `goimports` installed.
- `docs/INTERFACES.md` aligned to the Step-0 interface list; `docs/ROADMAP.md` rewritten from the
  milestones with acceptance criteria.

## Next
- Scaffold packages per ARCHITECTURE §10, `Makefile`, CI, `README.md`; first green
  `go build ./... && go vet ./... && go test ./...`; commit.
- Step 0.5: `cmd/authlab` lab binary (apps needed for M1 first: simple, multistep, modal, iframe,
  spa-jwt, hidden-entry, bound-session).

## Known issues
- None yet.

## Deferred
- None yet.

## How to run
```sh
make build          # go build ./...
make test           # go vet + go test ./...
make lint           # gofmt/goimports/vet checks
make lab            # run the local lab (cmd/authlab) on :8089
make e2e            # browser e2e against the lab (needs Chrome; set AGENTBROWSER_E2E=1)
```
