# RESEARCH — Prior Art Survey & Conclusions

> Scope: an **agent-driven browser** that authenticates with operator-provided credentials,
> handles verification/CAPTCHA, and browses an app like a normal user (no recon/discovery —
> that's a separate harness). Read through that lens: reliability of multi-variant auth and a
> live session, deterministic-first cost control, an audit trail, and running against
> **local/weak models**, not just frontier APIs.
>
> Verified against sources current to Nov 2026. This space moves fast; every "depend
> on it" below carries an implicit "re-check the release before you commit." (Some section
> notes still say "for this use case" — now read that as authenticate-and-browse.)

---

## 1. LLM browser agents

### browser-use (Python)
- **Does well:** Largest community and the usual default for custom agents (~108k stars, top of several WebVoyager/Odysseys leaderboards at ~87% with Claude). DOM + accessibility-tree perception with element indexing; good at "just do the task" autonomy.
- **Falls short for us:** Python, so it can't be our core (Go brief). Full-agent planning on every step is token-heavy and slow; weak/local models degrade badly because it leans on strong tool-calling. Autonomy-first means less determinism and a thinner audit story than a recon operator wants.
- **Verdict:** **Borrow ideas** — the indexed-DOM element model, the "interactive elements only" pruning, and the retry/self-correction patterns. Don't depend on it.

### Stagehand (TypeScript, on Playwright)
- **Does well:** `act` / `extract` / `observe` primitives layered on Playwright. More deterministic and debuggable than browser-use; cheaper per action because each action needs simpler reasoning than full planning. Caches resolved selectors so repeat runs skip the LLM.
- **Falls short for us:** TS/Node runtime; Playwright dependency. Primitive-level, so the higher-level recon orchestration is still on us to build.
- **Verdict:** **Borrow heavily** — the primitive split (observe → act → extract) and selector-caching are directly applicable to our deterministic-first skill layer. This is the closest philosophical match to what we want.

### Skyvern (Python, vision-first)
- **Does well:** Reasons over the rendered page with vision + set-of-marks; resilient to DOM churn and obfuscation; form-oriented; managed option with a dashboard.
- **Falls short for us:** Vision on every step is the most expensive and slowest path and the worst fit for local models. Managed/cloud framing conflicts with our local-only / redaction requirement.
- **Verdict:** **Borrow the vision fallback** (set-of-marks over screenshots) as an *escalation* path when structured perception fails, not as the default. Don't depend on it.

### Playwright MCP / Chrome DevTools MCP
- **Does well:** Accessibility-tree snapshot is the default perception mode and is cheap (~120 tokens vs ~1,500 for a screenshot, per secondary benchmarks). Standard MCP tool surface; hybrid (tree + selective screenshot) hit ~85% on WebGames vs ~50% for prior agents. Mature, Microsoft-maintained.
- **Falls short for us:** MCP streams the full tree into context every step (one Playwright benchmark: ~114k tokens via MCP vs ~27k via CLI) — brutal on context budget and local models. Node runtime. Tool surface is generic, not recon-shaped.
- **Verdict:** **Borrow the a11y-snapshot format** and the hybrid escalation finding. Consider *exposing* our own agent as an MCP server later (platform concern), but don't build the core on someone else's MCP.

### Anthropic computer use / OpenAI CUA-style
- **Does well:** Pure screenshot + coordinate actions; works on anything a human can see, including canvas/non-DOM UIs. Strong on frontier models.
- **Falls short for us:** Most expensive perception, needs a strong vision model, poor local-model story, coordinate clicks are brittle under layout shift, and no structured handles for reliable form-filling/clicks.
- **Verdict:** **Borrow as a last-resort action mode** behind the same `Action` interface. Never the primary.

**Cross-cutting conclusion:** The field has converged on *structured perception (a11y tree / indexed DOM) first, selective vision second*. The hybrid wins every head-to-head that controls for cost. Our differentiator is **deterministic-first** (don't call the model at all when a heuristic resolves it) plus a **recon-specific objective** (endpoint coverage, not task completion). No existing agent is built around coverage or an audit trail, so we borrow perception/action machinery and build the orchestration + recon engine ourselves.

---

## 2. Scope note — not a crawler/recon tool (removed)

> This project previously surveyed classic crawlers (Katana, gospider, hakrawler, gau) and
> Burp/Caido integration for an endpoint-discovery engine. **That scope is removed:** the tool
> now authenticates and **browses like a normal user**; any endpoint discovery, traffic analysis
> or inventory is handled by a **separate harness** (which the operator can point the browser's
> optional upstream proxy at — ADR 0007). The research below that still applies is: LLM browser
> agents (§1), perception (§3), provider abstraction (§4), and CAPTCHA (§5). No crawler is a
> dependency.

---

## 3. Perception approaches

| Approach | Token cost | Reliability | Weak/local-model fit |
|---|---|---|---|
| Raw DOM | Very high (noisy) | Medium (brittle selectors) | Poor (context blowout) |
| Accessibility tree | **Low** (~120 tok) | High for interactive elements | **Good** |
| Pruned/indexed DOM | Low–med | High | Good |
| Screenshot + set-of-marks | High (~1.5k tok) | High under DOM churn | Poor (needs vision) |
| Hybrid (a11y + selective vision) | Med | **Highest** (~85% WebGames) | Medium |

**Conclusion / pick:** **Hybrid, a11y-tree-first.** Default perception is the accessibility tree reduced to interactive + landmark nodes, each assigned a stable integer handle. Escalate to a screenshot with set-of-marks overlay **only** when (a) the task is inherently visual (CAPTCHA presence, canvas UI), or (b) structured perception failed to locate a target or the agent is detected as stuck. This keeps the common path cheap and local-model-friendly, and reserves the expensive vision call for where it earns its cost. This matches the WebGames hybrid result and keeps us off the MCP "full tree every step" anti-pattern by diffing and summarising state between steps.

---

## 4. Tool-calling & structured output across providers

- **The landscape is genuinely fragmented.** Every provider uses a different parameter name and request shape; schema features (recursive schemas, numeric/length constraints, `additionalProperties`) are unevenly supported.
- **Three tiers of capability:**
  1. **Native strict / constrained decoding** (OpenAI Structured Outputs strict mode, Gemini `response_schema`, Anthropic tool-use / `output_config` format) — schema-valid output guaranteed by token masking.
  2. **JSON mode** — syntactically valid JSON, no schema guarantee (soft constraint).
  3. **No reliable structured output** (many local/older models) — need prompt-coaxed JSON or grammar-constrained decoding.
- **Local models:** llama.cpp/vLLM expose **grammar-constrained decoding (GBNF / JSON-schema → grammar)** which is *stronger* than hosted JSON mode — we should exploit it when the endpoint supports it.
- **Normalisation advice from the field:** keep tool/output schemas in the **strictest common subset** — every field required, `nullable` instead of optional, `additionalProperties:false`, avoid numeric/length constraints and deep recursion. Libraries like Instructor prove the unified-interface pattern works.

**Conclusion:** Our `LLMProvider` abstraction must do **capability negotiation** and pick the best available structured-output mechanism per model, with a **degradation ladder**: native tool-calling → native JSON-schema → JSON mode + validate/repair → grammar-constrained (local) → ReAct-style text parsing as the floor. Author every tool schema to the common subset so the same schema works across all tiers. This is the single most important abstraction in the system for the "model-agnostic" requirement, and it's where a naive design will quietly break on local models.

---

## 5. CAPTCHA — what the research says, and our stance

- **Landscape:** reCAPTCHA still dominates (~65% of tracked sites), then Cloudflare Bot Management / Turnstile, hCaptcha, and Arkose/FunCaptcha for high-stakes signup. reCAPTCHA v3 and Turnstile are invisible/score-based; v2, hCaptcha, Arkose are interactive.
- **Detection is tractable and deterministic:** provider scripts, iframe origins, sitekeys, and DOM signatures identify the vendor reliably without an LLM. This is the part we build.
- **Our stance (by design):** the CAPTCHA subsystem **detects and classifies** in-core, then runs a **pluggable solver chain**. The default is **human-in-the-loop** (pause, surface, resume). Other strategies are **operator-supplied external plugins** (over gRPC) and **test-mode/known-sitekey** for the operator's benchmark lab. The core ships **no challenge-defeating logic**; whether automated solving is acceptable depends on the app's terms and the operator's authorisation, so it's an explicit, operator-owned, audited choice.

**Conclusion:** Build detection/classification in-core (`Detector`/`Injector`) + a pluggable `Solver` chain driven by a `Resolver` + human-handoff default. Treat automated solving as an external, operator's-responsibility plugin (in-process or gRPC), logged like any other action. Full plugin contract in ADR 0011.

---

## 6. Overall conclusions that shape the architecture

1. **Structured-perception-first, vision-as-escalation** is the settled winner. Build the hybrid.
2. **Deterministic-first is our edge.** Every borrowed agent calls the model too often; our win on cost, speed, reliability and local-model support comes from resolving forms/logins/navigation with code and only escalating to the LLM for genuine judgment.
3. **The job is authenticate + browse, not recon.** No crawler, inventory or exporter; endpoint discovery lives in a separate harness. Depth here means robust multi-variant auth, verification/CAPTCHA handling, and keeping a realistic session alive.
4. **The provider abstraction is the hard part of "model-agnostic"** — capability negotiation + a structured-output degradation ladder, schemas in the common subset.
5. **Operator-provided everything.** Credentials, verification codes/links, and CAPTCHA policy are the operator's; the agent drives the browser and asks when it needs something (VerificationProvider, CAPTCHA solver chain).

### Sources
Browser-agent comparisons: [joinmassive](https://www.joinmassive.com/blog/browser-use-vs-stagehand-vs-skyvern-choosing-an-agent-browser-framework), [skyvern alternatives](https://www.skyvern.com/blog/browser-use-alternatives/), [firecrawl](https://firecrawl.dev/blog/best-browser-agents), [dev.to agdex](https://dev.to/agdex_ai/ai-native-browser-automation-in-2026-browser-use-stagehand-steel-and-playwright-mcp-compared-160n). Perception: [tinyfish — what agents see](https://current.tinyfish.ai/issue/31/foundations/article/18062/what-browser-agents-see-before-they-think), [playwright.dev snapshots](https://playwright.dev/mcp/snapshots), [dev.to a11y vs screenshots](https://dev.to/pointchecknote/browser-automation-with-claude-playwright-mcp-why-accessibility-snapshots-beat-screenshots-2pke), [custodia 170x](https://dev.to/custodiaadmin/why-screenshot-mcps-cost-170x-less-than-playwright-mcp-and-when-that-matters-pj0). Go drivers: [libhunt chromedp vs playwright-go](https://www.libhunt.com/compare-chromedp-vs-playwright-go), [why-rod](https://redirect.github.com/go-rod/go-rod.github.io/blob/main/why-rod.md), [webscraping.fyi chromedp vs rod](https://webscraping.fyi/lib/compare/go-chromedp-vs-go-rod/). Structured output: [requesty](https://www.requesty.ai/blog/structured-outputs-across-llm-providers-the-compatibility-mess), [premai](https://blog.premai.io/llm-structured-output-from-json-mode-to-self-hosted-inference-complete-guide), [developersdigest](https://www.developersdigest.tech/blog/structured-outputs-across-ai-apis-2026). CAPTCHA/detection: [roundtable benchmark](https://research.roundtable.ai/bot-benchmarking/), [fingerprint](https://fingerprint.com/blog/captcha-alternatives/), [datadome](https://datadome.co/bot-detection/recaptchav2-recaptchav3-efficient-bot-protection/).
