# ADR 0013 — Exploration Model (Phase 2, design only): normal-user browsing + account-safety deny-list

**Status:** Accepted for **Phase 2 (build later)** · **Date:** 2026-11-15

> **Scope note:** v1 only authenticates and hands the session back — **no Explorer, no task mode,
> no deny-list.** This ADR designs the **Phase 2** authenticated crawl that starts from a v1
> `SessionBundle`. It is not built in v1; it's recorded now so v1's interfaces (perception, skills,
> budgets, audit, session restore + re-login) are chosen to support it.

## Context
After authenticating, the agent should exercise the app's main features realistically — enough
to look and behave like a normal logged-in user — without mapping, inventorying, or scoring it.
We need a simple notion of "what to do next" and "when to stop" that doesn't loop and doesn't
require a coverage metric.

## Decision

**`Explorer` proposes normal-user goals** from the **current state's unseen affordances** —
menus, links, items/detail views, forms, search boxes, pagination, settings toggles that haven't
been used yet. Exercising a feature means *using* it: navigate menus, open items, **fill and
submit forms with sensible, correctly-typed data**, run a search, paginate, apply a filter,
toggle a setting.

**Mode:** free-explore — pick the next unused affordance in the current view, biased toward breadth
(don't drill one list forever). (A plain-English **task mode** is a possible later addition; it
reuses the same loop and the deny-list below, but it's out of this phase's minimum.)

**State dedup (simhash):** `simhash(structural a11y signature + path_template)` within a Hamming
threshold; parameterised routes (`/item/1`, `/item/2`) collapse to one state so the agent doesn't
loop walking a list.

**Stop condition (simple):** budget exhausted, **or no new states for N iterations**. No coverage
metric, no inventory — just a short run summary (states visited, session kept alive).

**Keep the session alive:** the session health check runs during browsing; a detected logout or
expiry hands off to the `Authenticator` for re-login and the in-flight sub-goal restarts from its
start.

**Account-safety deny-list (the one guardrail Phase 2 adds).** Because the crawl acts
autonomously, it must not take **account-ending actions**: **logout, delete account, change
password/email, revoke sessions/API keys, deactivate account**. These are enforced at an
**action-execution boundary** (a `core/Executor` introduced in Phase 2) that every action producer
passes through — deterministic matcher + LLM classifier for ambiguous controls; a denied action is
skipped and recorded. This is **not** scope (there's still no host allowlist) and it's about
*actions*, not hosts. Ordinary app use (creating/editing content, navigating) is not blocked.

## Rationale
- "Behave like a normal user" is a usage goal, not a measurement goal, so a frontier/coverage
  metric is the wrong tool — unused-affordance selection + simhash dedup is enough to move
  around realistically without looping.
- Task mode reuses the exact same loop and guardrails, so it's a thin addition, not a second
  engine.
- A dead-simple stop condition avoids the over-engineering the old plateau/coverage model
  carried.

## Consequences
- Needs a typed-value generator so form submits use sensible data (emails look like emails, dates
  like dates) — shared with the auth flow handlers.
- The state graph persists only for dedup and resumability, not for inventory (DATA_MODEL).
- **Spike:** the simhash threshold needs tuning against apps with known state counts, or the
  agent either loops or merges distinct views (RISKS).

## Rejected alternatives
- **Coverage/frontier scoring, static↔dynamic goals, auth matrix (old model):** recon machinery;
  removed with the discovery engine.
- **Crawl-everything:** loops on lists, wastes budget, and isn't "normal user" behaviour.
- **No dedup:** the agent re-visits the same list pages forever.
