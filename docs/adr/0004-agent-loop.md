# ADR 0004 — Agent Loop: deterministic skill layer + plan/act/verify fallback

**Status:** Accepted · **Date:** 2026-11-15

## Context
Control-loop shape determines reliability and cost. Options: single ReAct loop (one model
call picks the next action each step), planner/executor/verifier split, or hierarchical
(goal → sub-goal → action). A pure ReAct loop calls the model every step — expensive,
slow, and flaky on local models. We want determinism and auditability.

## Decision
**Deterministic-first, hierarchical-lite plan/act/verify.**

1. **Goal selection.** In **v1** the goal is driven by the **current auth-flow step** inside the
   `Authenticator` (ADR 0010) — find the login/signup form, advance a multi-step flow, enter a
   verification code, solve a CAPTCHA. In **Phase 2** goals come from the Explorer (ADR 0013). The
   loop does not generate its own browsing goals in v1.
2. **Skill layer first:** for the current `(state, goal)`, try registered deterministic Skills
   (`dismiss_cookie_banner`, `fill_typed_form`, `advance_multistep`, …). A Skill that `CanHandle`
   runs with **no planner LLM call** (at most one cheap classification). **Authentication is owned
   by the `Authenticator` service** (ADR 0010), which owns login/signup/verify *and* CAPTCHA/human
   handoff (ADR 0011) end to end — so CAPTCHA has exactly one owner.
3. **Planner fallback:** only when no Skill applies or a Skill escalates, call the Planner LLM to
   emit the next typed Action(s). Strong model, budgeted, structured output.
4. **Act:** in v1 the typed Action runs directly on the Page (audited; **no scope, no deny-list**).
   Phase 2 introduces a `core/Executor` boundary for the account-safety deny-list (ADR 0013).
5. **Verifier:** mostly deterministic predicates (URL changed? expected node present? error
   signature? logged out?); escalates to a cheap LLM only for ambiguous "did it work?" calls.
   Detects stuck/loop/error/logout and drives self-correction.

Self-correction: on repeated no-progress (same state_hash N times), the loop escalates —
retry with vision perception → try an alternate Skill → ask the Planner for a recovery
plan → (last resort) mark the current goal blocked and move on. A detected logout or a
failed session health check hands control back to the `Authenticator` for automatic
re-login (ADR 0010 §session lifecycle), then the in-flight sub-goal restarts from its start.

## Rationale
- The deterministic skill layer is our cost/reliability edge: logins and signup forms are
  ~90% pattern-matchable, so the model is spent only on genuine judgment (which flow is
  signup, what to explore, is this an error). This is what makes local models viable and
  runs cheap.
- Plan/act/verify (vs bare ReAct) gives a clean audit trail — each decision is a typed,
  logged step with a rationale — and a natural place to inject the deny-list/scope/budget checks.
- Hierarchical-lite (explorer or task goal → sub-goals → actions) matches both free-explore and
  task mode without the overhead of a full multi-level planner on every micro-step.

## Consequences
- Up-front work building and maintaining the Skill library and robust verifier predicates.
  This is deliberate: it's where reliability is won, and it degrades gracefully (missing
  Skill → Planner handles it, just more expensively).
- Every LLM call in the loop has a stated purpose, input budget and fallback (see each
  Skill/Planner/Verifier doc comment). Calls without one are a code-review smell.
- Loop state (current goal, visited hashes, current node, in-flight sub-goal) is checkpointed
  for resumability.

## Rejected alternatives
- **Single ReAct loop:** simplest, but model-call-per-step cost, weak audit, poor local
  support, and loops/gets-stuck without a structured verifier.
- **Full hierarchical planner (goal→subgoal→action at every level, all LLM):** more robust
  on paper for very long horizons, but heavy token cost and latency for our mostly-short
  flows; overkill vs the skill-layer approach.
- **Separate planner *process*:** unnecessary complexity; it's a module, not a service.
