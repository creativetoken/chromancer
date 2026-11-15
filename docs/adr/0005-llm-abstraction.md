# ADR 0005 — LLM Abstraction: one interface, capability negotiation, structured-output ladder

**Status:** Accepted · **Date:** 2026-11-15

## Context
"Model-agnostic — swapping providers is a config change, not a code change" is a core
requirement. Providers differ sharply: tool-calling shapes, structured-output mechanisms
(strict/constrained vs JSON-mode vs none), vision support, streaming, token accounting
(RESEARCH §4). Local models (Ollama/vLLM/llama.cpp) add grammar-constrained decoding and
often weak/absent native tool calling.

## Decision
A single Go `LLMProvider` interface (INTERFACES §model) with **capability negotiation** and
a **structured-output degradation ladder** inside each implementation.

- **Implementations:** `anthropic`, `openai`, `gemini`, `ollama`, and a generic
  `openaicompat` (covers vLLM, llama.cpp server, LM Studio, OpenRouter, Azure, etc.).
- **Capability negotiation:** each provider reports `Capabilities` from config + a one-time
  probe. The planner/skills branch on it: drop vision if unsupported (route visual steps to
  a vision-capable model or ask the operator); downgrade tool-calling to the ladder below.
- **Structured-output ladder** (strongest available first):
  1. native tool calling →
  2. native JSON-schema / constrained decoding →
  3. JSON mode + schema-validate + one repair round →
  4. grammar-constrained decoding (local GBNF / JSON-schema→grammar) →
  5. ReAct-style text parsing (regex/heuristic) as the floor.
  The implementation returns **normalised `ToolCall`s regardless of tier**, so the loop is
  oblivious to how structured output was obtained.
- **Schemas authored to the strictest common subset:** all fields required, `nullable` not
  optional, `additionalProperties:false`, no numeric/length constraints, shallow nesting —
  so one schema works across all tiers.
- **Model routing** (INTERFACES `Router`): per-task model selection from config —
  cheap/local for `Classify`, strong for `Plan`, vision-capable for `Vision`, etc.
- **Token/cost accounting** per call via `Usage`, charged to the Budget and audited, using
  a per-model price table in config.
- **Prompts & tool schemas are versioned** (content-hashed, stored with the run's
  `config_hash`) with optional per-model prompt variants where a model needs different
  phrasing.

## Rationale
- The ladder is the only honest way to be "model-agnostic" down to local models — a naive
  "assume tool calling" design silently breaks on half the targets we want to support.
- Normalising at the provider boundary keeps the entire rest of the system provider-blind.
- Routing + price tables make the cheap/strong/vision split a config decision, satisfying
  the requirement directly and enabling the eval harness to A/B models on identical tasks.

## Consequences
- Each provider impl is non-trivial (it owns the ladder + repair). This is the single most
  important abstraction; it earns the investment.
- We must maintain a price table and capability probes as models change — small, data-only.
- **Spike flagged:** a11y-tree + tool-ladder on a 7–8B local model (e.g. via vLLM grammar
  constraints) — prove the floor works before promising full local-only operation.

## Rejected alternatives
- **Depend on an existing abstraction lib (Instructor-style):** Python/TS, not Go; and we
  need capability negotiation + routing + budgets integrated, not just structured output.
- **Target one provider, adapt later:** violates the core requirement and bakes in
  tool-calling assumptions that are painful to unwind.
- **Lowest-common-denominator (ReAct text only, everywhere):** portable but throws away the
  reliability of native tool calling on strong models.
