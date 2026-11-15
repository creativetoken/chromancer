# Planning & Architecture Brief: Agent-Driven Browser Recon for Bug Bounty

## Your role

You are a principal engineer and offensive-security tooling architect. Your job in this session is to **research, plan and design**. You are not implementing yet. You will produce a complete architecture and a phased delivery plan for an autonomous, LLM-driven browser agent used for **authorised** bug bounty reconnaissance.

Think like someone who has shipped browser automation at scale and has also run real bug bounty recon. Be opinionated, justify every major decision, and call out where you are uncertain.

---

## Project goal

Build a tool where an AI agent drives a real browser against a target web application to:

1. **Phase 1 – Explore & authenticate:** crawl the app, find the signup and login flows (including multi-step, SSO-style, modal-based and SPA-routed ones), create an account, verify it if needed (email link/OTP), log in, and keep a reusable authenticated session.
2. **Phase 2 – CAPTCHA handling:** detect and deal with CAPTCHAs met during signup and login, through a pluggable, policy-controlled strategy chain.
3. **Phase 3 – Authenticated discovery:** use the app the way a real user would while logged in, and build a full inventory of endpoints, methods, parameters, auth requirements and object identifiers for later testing.

It must be **model-agnostic**. Swapping between Anthropic, OpenAI, Google, local models (Ollama, vLLM, llama.cpp) or any OpenAI-compatible endpoint should be a config change, not a code change.

**Preferred language: Go.** Evaluate Go-native browser options (e.g. `chromedp`, `go-rod`, `playwright-go`) against the alternatives. If you conclude that a specific part is far better served by another runtime (for example a Python or Node sidecar), say so explicitly and justify the cost of a polyglot system.

---

## Non-negotiable guardrails (design these in, not bolted on)

This tool targets systems the operator is authorised to test. The architecture must enforce that:

- **Destructive-action gate:** the agent classifies actions by risk. Anything state-changing beyond its own test account needs explicit policy approval or human confirmation.
- **Audit log:** an immutable, timestamped record of every action, request and LLM decision, suitable for attaching to a report or showing a program that you stayed in scope.
- **Kill switch & budgets:** global and per-run limits on time, requests, LLM tokens/cost and accounts created.

---

## What I need you to research first

Before designing anything, survey the prior art and summarise what to borrow and what to avoid. Verify current state, because this space moves fast:

- LLM browser agents: browser-use, Stagehand, Skyvern, Playwright MCP, Anthropic computer use / OpenAI CUA-style approaches, and any newer notable projects.
- Classic crawlers and recon tools: Katana, gospider, hakrawler, Burp/Caido crawlers, ZAP AJAX spider, and JS endpoint extractors (LinkFinder-style, sourcemap mining).
- Perception approaches: raw DOM vs accessibility tree vs screenshot plus set-of-marks vs hybrid. Weigh token cost, reliability and performance with weaker/local models.
- How tool-calling and structured output differ across providers, and how to normalise them, including fallback for models with weak or no native tool calling (JSON-mode, grammar-constrained decoding, ReAct-style parsing).

For each, give 2–4 bullets: what it does well, where it falls short for *this* use case, and whether to depend on it, borrow ideas from it, or ignore it.

---

## Architecture requirements to address

Cover each of these explicitly. Where there are real alternatives, give options with trade-offs and then pick one.

### 1. Agent core
- Control loop design: planner / executor / verifier split vs single ReAct loop vs hierarchical (goal → sub-goal → action). Justify for reliability and cost.
- Action space: a minimal, typed set of browser actions (navigate, click, type, select, scroll, wait, upload, back, extract, assert) plus higher-level skills (e.g. `fill_signup_form`, `complete_email_verification`).
- Deterministic-first principle: use cheap heuristics and code (form detection, login-form classifiers, known patterns) wherever possible, and only call the LLM where judgment is needed. Show where that line sits.
- State and memory: page-state graph (states = app views, edges = actions), visited-state dedup (DOM similarity hashing), working memory vs long-term run memory, and resumability after a crash.
- Self-correction: detecting being stuck, loops, error pages, logouts, and recovering from them.

### 2. Model abstraction layer
- A Go interface for LLM providers covering chat, tool calling, structured output, vision input, streaming and token accounting.
- Capability negotiation: the agent adapts if a model lacks vision or reliable tool calls.
- Model routing: different models per task (cheap/local for classification, strong for planning, vision for CAPTCHA/visual tasks), set in config.
- Prompt and tool-schema management: versioned prompts, per-model prompt variants where needed.
- Evaluation hooks so models can be compared on the same benchmark tasks.

### 3. Browser layer
- Engine choice and driver (CDP directly vs Playwright protocol). Headful vs headless, browser contexts per identity, persistent profiles.
- Fingerprint and bot-detection considerations: what is reasonable for a legitimate tester to configure (realistic UA, viewport, timezone, persistent profile) and where the line sits.
- Robust element targeting: stable IDs from the a11y tree, fallbacks, iframes, shadow DOM, popups/new tabs, file uploads, downloads.
- Waiting and stability detection for SPAs (network idle, DOM mutation settling, route changes).

### 4. Identity & account management
- Credential vault (encrypted at rest) holding generated identities per target: email, username, password, phone if needed, plus session artefacts (cookies, localStorage, tokens).
- Email verification: catch-all domain with IMAP/API inbox polling, OTP and magic-link extraction. Design an interface so the mail provider is pluggable.
- SMS/phone verification: treat as out of scope by default, with a human-in-the-loop hook.
- Multiple accounts and roles per target (needed later for IDOR/authz testing): design for this from day one.
- Session health checks and automatic re-login when a session expires.

### 5. CAPTCHA subsystem (Phase 2)
- Detection and classification: reCAPTCHA v2/v3, hCaptcha, Cloudflare Turnstile, FunCaptcha/Arkose, image/text CAPTCHAs, custom challenges.

### 6. Traffic capture & discovery engine (Phase 3)
- Intercept all traffic (CDP network events and/or an upstream MITM proxy). Support chaining to Burp Suite or Caido so findings show up in the operator's normal workflow.
- Endpoint and parameter extraction from: observed requests, forms, links, JS bundles (static analysis plus sourcemaps), GraphQL (introspection where allowed, plus observed operations), WebSockets, OpenAPI/Swagger leaks, `robots.txt`/sitemaps.
- Normalisation: path templating (`/users/123` → `/users/{id}`), parameter typing, method/content-type, auth requirement (anonymous vs each role), and recognising object identifiers.
- Coverage-driven exploration: the agent should *intentionally* exercise features (settings pages, CRUD flows, search, file upload, API-backed widgets) to find new endpoints. Define a coverage metric and use it to steer the agent.
- Output formats: an OpenAPI 3 spec, JSONL inventory, HAR, and exports shaped for downstream tools (ffuf, nuclei, Burp sitemap import).

### 7. Platform concerns
- Project layout and module boundaries (Go packages), the key interfaces, and the data model (SQLite vs Postgres. Justify).
- Config format and per-target workspace layout.
- Concurrency model: multiple targets/identities in parallel, with per-target rate limiting.
- Observability: structured logs, traces of agent decisions, screenshots/video per step, replayable runs.
- Interfaces: CLI first. Later options: API server, MCP server exposure, web UI. State which and when.
- Security of the tool itself: secrets handling, sandboxing the browser, not leaking target data to LLM providers without consent (support redaction and local-only mode).

### 8. Testing & evaluation
- A local benchmark lab: OWASP Juice Shop, crAPI, DVWA, and **custom mock apps you design** that cover hard cases (multi-step signup, email verification, SPA routing, login in a modal/iframe, OAuth-style redirect, CAPTCHA in test mode, rate limits, session expiry).
- Metrics: auth success rate, steps/tokens/cost per successful login, endpoint coverage vs ground truth, scope-violation count (must be zero), runtime.
- How to compare models and prompts reproducibly.

---

## Deliverables from this session

Produce these as markdown files in a `docs/` folder:

1. **`docs/RESEARCH.md`**: prior-art survey and the conclusions you drew.
2. **`docs/ARCHITECTURE.md`**: system overview, a component diagram (Mermaid), data flow for each phase, sequence diagrams for "discover and sign up", "login with CAPTCHA" and "authenticated discovery loop".
3. **`docs/adr/`**: one Architecture Decision Record per major choice (language/runtime, browser driver, perception strategy, agent loop, LLM abstraction, storage, proxy integration, CAPTCHA strategy).
4. **`docs/INTERFACES.md`**: Go interface definitions (signatures and doc comments only) for: `LLMProvider`, `Browser`/`Page`, `Action`, `Perceiver`, `Planner`, `ScopeGuard`, `PolicyEngine`, `IdentityVault`, `MailProvider`, `CaptchaSolver`, `TrafficSink`, `EndpointStore`, `Exporter`.
5. **`docs/DATA_MODEL.md`**: entities, relationships, and schemas for the endpoint inventory and the state graph.
6. **`docs/ROADMAP.md`**: milestones for Phases 1–3, each with scope, acceptance criteria, demo scenario against the benchmark lab, and rough effort. Start with a thin vertical slice (crawl → find login → log in with given credentials against one target) before broadening.
7. **`docs/RISKS.md`**: technical, cost, legal/program-policy and reliability risks, each with a mitigation.
8. **`docs/OPEN_QUESTIONS.md`**: decisions you need me to make, each with your recommended default.

---

## How to work

- Research first, then propose **2–3 candidate architectures** at a high level, compare them in a table (reliability, cost per run, local-model friendliness, complexity, extensibility), recommend one, and only then go deep on it.
- Prefer boring, proven components over clever ones. Minimise dependencies in the core.
- Every LLM call in the design should have a stated purpose, input budget and fallback.
- Flag anything you would spike/prototype before committing to (e.g. "a11y tree vs screenshot perception with a 7B local model").
- Don't write implementation code beyond interface definitions and tiny illustrative snippets.
- If anything in this brief is ambiguous or contradictory, list it in `OPEN_QUESTIONS.md` with your assumed default and carry on rather than stopping.
