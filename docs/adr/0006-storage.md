# ADR 0006 — Storage: SQLite per-target workspace (Postgres later, same interfaces)

**Status:** Accepted · **Date:** 2026-11-15

## Context
We persist: scope, runs, the hash-chained audit log, the state graph (for dedup +
resumability), operator credentials/sessions (encrypted), and the budget ledger. Access is
single-operator, mostly single-writer per account, with occasional parallel runs across
*different* accounts. Choice: SQLite vs Postgres (vs embedded KV).

## Decision
**One SQLite database file per account workspace**, accessed through the `store` interfaces
(state-graph store, audit `Log`, `IdentityVault`, `SessionStore`). Session blobs
(cookies/storage) are stored encrypted in the DB (small); any larger artefact goes to a
sidecar file referenced by path. WAL mode on; a single writer goroutine per account serialises
writes.

Postgres is a **future, optional** backend behind the same interfaces, introduced only if/
when multi-operator or heavy multi-account concurrency demands it.

## Rationale
- **Isolation & portability:** an account = a directory with its DB + session files. Trivial
  to back up, hand over, or delete when done. No server to run.
- **Workload fit:** per-account writes are effectively single-writer; SQLite WAL handles this
  with room to spare. Cross-account parallelism is separate files = no contention.
- **Audit integrity:** the hash chain lives in a plain table; SQLite's durability + our
  "append-before-act" rule give the guarantees we need without a DB server.
- **Boring & proven** (project principle). One CGo-free driver, no ops.

## Consequences
- No cross-account queries in one SQL statement (fine — summaries aggregate in app code).
- Concurrency within an account is cooperative (writer goroutine + WAL readers); our
  concurrency model parallelises across accounts, not conflicting writers on one table.
- Encryption: secret blobs are encrypted at the app layer before insert (the DB file itself
  may optionally sit on an encrypted volume / SQLCipher if the operator wants at-rest DB
  encryption — config option, not required).
- Migration path to Postgres is schema-compatible because all access is interface-mediated.

## Rejected alternatives
- **Postgres from day one:** adds a server, auth, and ops for zero benefit at single-
  operator scale; worse portability; against "boring, minimal dependencies."
- **Embedded KV (bbolt/badger):** fine for simple key lookups, but the state graph and
  account/session relations are more naturally SQL; little upside over SQLite here.
- **One shared DB for all accounts:** couples unrelated accounts and complicates isolated
  deletion; per-account files are cleaner.
