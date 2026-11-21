# CAPTCHA solving

Automated solving is the norm; **human handoff is the last resort**. The agent
detects a challenge, runs a capability-prefiltered chain of solvers within a
budget, applies the result through a core Injector, submits, and checks
acceptance. Only when the whole chain is exhausted (or a genuine block/loop is
hit) does it escalate to a human.

Core owns all browser contact (`Detector`, `Injector`); **solvers only solve** —
they receive a `Challenge` and return a `Result`, never touching the page.

In agent-only mode (ADR 0017) the LLM agent triggers this pipeline with a single
`solve_captcha` tool on the current page; detection, the solver chain, injection
and the acceptance check all run exactly as below. The agent decides *when* to
solve (typically after filling the form, before submitting) and escalates to a
human via `request_human` when the page is a bot-wall rather than a solvable
challenge.

## Detection (`internal/captcha/detect.go`)

One **unified detector** runs a single DOM probe and a pure classifier. The same
classifier runs on:

- the **live DOM** (a JS probe that judges visibility by *computed style* — not
  bounding-box size, because an invisible reCAPTCHA still renders a visibly-sized
  badge and a hidden full-width challenge iframe);
- **saved HTML fixtures** and **failed-submit response bodies** (a pure-Go
  `x/net/html` probe), so detection is unit-tested without a browser.

### Classes and extraction

| Class | Detected from | Extracted |
|---|---|---|
| reCAPTCHA v2 checkbox | visible `.g-recaptcha` widget | sitekey, callback, data-s |
| reCAPTCHA v2 invisible | `data-size="invisible"` widget | sitekey (→ **score**, non-blocking) |
| reCAPTCHA v3 / Enterprise | `api.js?render=<key>` / `grecaptcha(.enterprise).execute(...)` | sitekey, action, enterprise (→ score) |
| hCaptcha | visible `.h-captcha` widget | sitekey, callback, rqdata |
| Turnstile | visible `.cf-turnstile` (managed/non-interactive) or invisible (score) | sitekey, action, cData |
| Cloudflare interstitial | "Just a moment…" + challenge-platform script | classify only |
| image grid | `data-captcha="grid"` / `.rc-imageselect` with ≥6 tiles | tile count, input selector |
| text CAPTCHA | a captcha-labelled `<img>` + a short text input | image + answer-input selectors |
| Arkose/FunCaptcha, GeeTest | known script/iframe origins | classify → **human handoff** |

Detection also fires from **script/network signals** and from a **failed-submit
response body** (a login POST rejected with a captcha that was not on the
original page). Score/invisible challenges are **non-blocking**: the page runs
them on submit and attaches the token; they are never a human handoff.

Fixtures: `testdata/captcha/fixtures/` (one per class + non-captcha decoys);
tests in `internal/captcha/detect_test.go` assert classification, extraction and
**zero false positives**.

## The chain

Order (default): `testmode → native → vision → external → human`.

1. **testmode** — answers the vendors' **published test sitekeys** (reCAPTCHA
   v2, hCaptcha, Turnstile) with their documented always-pass tokens, plus the
   operator's lab tokens. Declines real keys (it defeats nothing real). Instant,
   free.
2. **native** — lets the real browser complete the challenge: clicks the
   widget's checkbox (from the detected widget bounds) and the resolver waits for
   the response field to fill. Solves low-risk v2 checkboxes and managed
   Turnstile; declines when nothing fills.
3. **vision** — image/text CAPTCHAs via a vision model (the `ModelRouter`, so
   model-agnostic). Text → typed answer; image grid → tile clicks, re-detected
   each round. Applies a confidence threshold; tracks per-model accuracy.
   This solver **declines unless the configured model reports `Vision`** — the
   hosted providers (Claude 3+, GPT-4o+, Gemini 1.5+) enable it by default when
   their provider is selected; the `mock` provider does not. Force it on or off
   with `AGENTBROWSER_LLM_VISION=1`/`0`. With vision off, image/grid challenges
   have no automated solver and fall straight through to **human**.
4. **external** — out-of-process solver plugins (`plugins/captcha-generic/`): a
   generic adapter for the common **create-task → poll → report** service API,
   configured by endpoint/API-key/vendors/price; forwards sitekey, page URL,
   action, enterprise, UA and proxy; records cost; reports good/bad on `Outcome`.
5. **human** — `HumanHandoff`, unchanged, always last.

A **fresh challenge is re-detected per retry** (tokens are single-use). A solve
that declines advances to the next solver; a human handoff happens only when the
chain is exhausted. Budgets (solves, cost, time) and audit redaction apply.

### Injection & acceptance (`internal/captcha/inject.go`)

The Injector sets every matching response field, invokes the page-registered
success callback (named global from `data-callback`, incl. `obj.fn`), and types
text answers into the text-captcha input. A solve is **accepted** only when (a)
the token/answer was observed in an outgoing request
(`Recorder.TokenSubmitted` — the reached-backend proof) **and** (b) the challenge
is gone; otherwise it is reported rejected and the loop re-detects a fresh
challenge.

### Stats & adaptive order

`Stats` persists per-`(target, vendor, solver)` attempts, accepted, median
latency and cost (file-backed). With `adaptive_order: true` the resolver reorders
eligible solvers by observed success rate (ties → cheaper/faster). Default off.

## Chain config

Point `AGENTBROWSER_CAPTCHA_CONFIG` at a YAML file
(see `configs/captcha.example.yaml`):

```yaml
captcha:
  chain: [testmode, native, vision, external, human]
  max_solves: 5
  max_cost: 0.50
  timeout_per_solver: 90s
  adaptive_order: false
  external:
    - name: generic
      path: ./bin/captcha-generic
      endpoint: ${SOLVER_ENDPOINT}
      api_key_env: SOLVER_API_KEY
      vendors: [recaptcha_v2, recaptcha_v3, hcaptcha, turnstile]
      needs_proxy: true
      price_usd: 0.002
```

Omitting a category disables it; **human handoff is always appended last** and
cannot be removed.

## Writing a solver plugin

A solver only solves and runs out-of-process. Implement `captcha.Solver`
(`Name`, `Describe`, `Solve`, `Report`) and serve it over the stdio-JSON
protocol with `plugin.Serve(mySolver, os.Stdin, os.Stdout)`. The normative wire
schema is `internal/captcha/plugin/proto/solver.proto`; the Go transport mirrors
it. Every plugin must pass `plugin.Conform`. The generic adapter in
`plugins/captcha-generic/` is a complete worked example (create/poll/report).
Point `AGENTBROWSER_SOLVER_PLUGIN` (or a config `external` entry) at the binary.

## Adding a new vendor

1. **Detector**: add the widget/script signatures to the probe + classifier in
   `detect.go` (and the pure-Go path for fixtures). Judge visibility by computed
   style; invisible/score variants must classify as `KindScore`.
2. **Injector**: add the response-field selector(s) and callback convention in
   `inject.go` (the generic injector usually suffices).
3. **Fixtures**: add a realistic HTML fixture (and a non-captcha decoy) under
   `testdata/captcha/fixtures/` and a case in `detect_test.go`.
4. **Lab mock**: add a mock app in `internal/lab/apps_captcha.go` reproducing the
   vendor's DOM + a verify endpoint, and an e2e in `e2e/captcha_vendors_e2e_test.go`.

## Lab apps (offline e2e)

`internal/lab/apps_captcha.go`: `mock-recaptcha-v2`, `mock-recaptcha-invisible`,
`mock-recaptcha-v3`, `mock-hcaptcha`, `mock-turnstile-managed`,
`mock-turnstile-invisible`, `image-grid`, `text-captcha`, `captcha-hard`
(rejects everything → handoff), `captcha-on-failed-submit`. The lab is fully
offline — mock widgets never load real vendor CDN scripts. The mock external
solver service used by the generic adapter is a local `httptest` server
(`plugins/captcha-generic/solver_test.go`); no real service is ever contacted.

## Security / scope

- No real solver service is called in tests; no real keys are embedded.
- Browser fingerprint spoofing / stealth patching is **out of scope**.
- Arkose/FunCaptcha and GeeTest are detected and handed off, not solved.
- Tokens are redacted before every audit-log write.
