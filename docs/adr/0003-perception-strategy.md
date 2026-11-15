# ADR 0003 — Perception: accessibility-tree-first, screenshot+set-of-marks on escalation

**Status:** Accepted · **Date:** 2026-11-15

## Context
How the agent "sees" a page drives token cost, reliability, and whether weak/local models
work at all. Options: raw DOM, accessibility tree, pruned/indexed DOM, screenshot +
set-of-marks, or a hybrid. Research (RESEARCH §3) shows the hybrid (a11y + selective
vision) is the reliability leader (~85% WebGames vs ~50%) while structured perception is
far cheaper (~120 vs ~1,500 tokens) and the only path that works on local models.

## Decision
**Default perception = accessibility tree**, reduced to interactive + landmark nodes, each
given a **stable integer handle**, serialised within a configurable token budget and
**diffed between steps** (send the delta + a compact summary, not the whole tree every
step). **Escalate to a screenshot with a set-of-marks overlay** (numbered boxes over the
same handles) only when `Perceiver.NeedsVision` is true: inherently visual task (CAPTCHA
presence, canvas/drag UI), structured perception failed to locate a target, or the verifier
flagged "stuck". Coordinate-only clicking is the last resort inside the vision path.

## Rationale
- Cheap common path keeps per-step cost low and makes local models viable — our hard
  constraint that most frameworks ignore.
- Diffing avoids the Playwright-MCP "full tree every step" blowup (~114k tokens) that kills
  context budgets and local models.
- Set-of-marks reuses the *same handles* as the a11y tree, so vision and structured actions
  share one target space — the planner doesn't need two mental models, and escalation is
  seamless.
- Escalation (not always-on vision) reserves the expensive, strong-model-only call for the
  ~10–20% of steps that genuinely need pixels.

## Consequences
- We must build: a11y extraction + pruning, stable-handle assignment that survives minor
  DOM churn (anchor on role+name+structural path, not raw node id), inter-step diffing, and
  the set-of-marks renderer.
- Handle stability is the main risk — if handles churn, the planner's references break.
  Mitigation: anchor handles semantically and re-resolve on each snapshot; **flag a spike**
  on handle-stability under SPA re-renders.
- Model routing: vision escalation requires a vision-capable model; the router must have
  one configured or the agent degrades to "ask operator" for visual-only steps (capability
  negotiation, ADR 0005).

## Rejected alternatives
- **Raw DOM:** noisy, token-heavy, brittle selectors — poor local-model fit.
- **Screenshot-first (Skyvern/CUA):** most expensive, needs a strong vision model every
  step, and no structured handles for reliable interaction. Kept only as the escalation floor.
- **Pure a11y, no vision ever:** fails on canvas/CAPTCHA/visual-only UIs and can't self-
  recover when the tree is misleading.
