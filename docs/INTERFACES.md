# INTERFACES

> Signatures and doc comments only — no implementations. v1 = authenticate and hand the session
> back. Author tool/output schemas to the **strictest common subset** so the same schemas work
> from frontier APIs down to grammar-constrained local models. `context.Context` carries the
> budget/kill-switch deadline and the run/trace IDs. **No ScopeGuard/Executor in v1** (no scope
> enforcement, no deny-list — the deny-list returns in Phase 2, §ARCHITECTURE 9).

Interfaces: `JobManager`, `Authenticator`, `EntrypointDiscovery`, `FlowHandler`, `SuccessDetector`,
`VerificationProvider`, `Detector`, `Injector`, `Resolver`, `Solver` (+ `.proto`), `HumanHandoff`,
`AuthRecorder`, `SessionBundle`, `ReplayChecker`, `ArtefactWriter`, `CredentialStore`, `Perceiver`,
`LLMProvider`, `ModelRouter`, `Budget`, `AuditLog`. Supporting: `Browser`/`Page`, `Skill`,
`Planner`, `Verifier`.

---

## model — LLMProvider, ModelRouter

```go
package model

// Capabilities describes what a concrete model/endpoint supports (config + one-time probe).
type Capabilities struct {
    NativeToolCalling  bool
    NativeJSONSchema   bool // strict schema-constrained output (constrained decoding)
    JSONMode           bool // syntactic-JSON-only guarantee
    GrammarConstrained bool // GBNF/JSON-schema→grammar (llama.cpp/vLLM)
    Vision             bool
    Streaming          bool
    MaxContextTokens   int
    MaxOutputTokens    int
}

type Message struct {
    Role    Role // system | user | assistant | tool
    Text    string
    Images  []ImagePart // only sent to models whose Capabilities.Vision is true
    ToolUse []ToolCall
    ToolRes []ToolResult
}

// Tool schema is JSON Schema in the common subset (all fields required, nullable not optional,
// additionalProperties:false, no numeric/length constraints, shallow nesting).
type Tool struct {
    Name, Description string
    Schema            json.RawMessage
}

type Usage struct {
    InputTokens, OutputTokens int
    CostUSD                   float64
}

// LLMProvider is the single abstraction that makes the agent model-agnostic. Implementations:
// Anthropic, OpenAI, Gemini, Ollama, generic OpenAI-compatible.
type LLMProvider interface {
    Name() string
    Capabilities() Capabilities
    // Chat emulates tool calling via the best mechanism when the model lacks it (JSON-schema →
    // JSON-mode+repair → grammar → ReAct text), returning normalised ToolCalls regardless.
    Chat(ctx context.Context, msgs []Message, tools []Tool, opts ChatOpts) (*Completion, error)
    // Structured returns one schema-valid object, falling back generate→validate→repair.
    Structured(ctx context.Context, msgs []Message, schema json.RawMessage, opts ChatOpts) (json.RawMessage, *Usage, error)
    Stream(ctx context.Context, msgs []Message, tools []Tool, opts ChatOpts) (<-chan Chunk, error)
}

// ModelRouter selects a provider per task from config (cheap/local for Classify, strong for
// Plan, vision-capable for Vision). Selection is logged.
type ModelRouter interface {
    For(task TaskKind) LLMProvider // Classify | Plan | Verify | Vision
}
```

---

## perceive — Perceiver

```go
package perceive

// Perceiver turns a live Page into a compact, token-budgeted state. Default impl is
// a11y-tree-first; a set-of-marks impl is used on vision escalation.
type Perceiver interface {
    Perceive(ctx context.Context, p browser.Page, budget PerceptionBudget) (State, StateHash, error)
    // NeedsVision reports whether structured perception is insufficient (canvas UI, suspected
    // CAPTCHA, prior step failed to locate a target).
    NeedsVision(s State, goal Goal, lastErr error) bool
}
```

---

## core — Action, Skill, Planner, Verifier

```go
package core

// ActionKind is the minimal typed action space. The LLM only ever emits these (never code).
type ActionKind string
const (
    Navigate ActionKind = "navigate"
    Click    ActionKind = "click"
    Type     ActionKind = "type"
    Select   ActionKind = "select"
    Scroll   ActionKind = "scroll"
    Wait     ActionKind = "wait"
    Upload   ActionKind = "upload"
    Back     ActionKind = "back"
    Extract  ActionKind = "extract"
    Assert   ActionKind = "assert"
)

// Action is a typed, serialisable instruction (a11y node handle preferred; coordinates for the
// vision fallback). In v1 actions run directly on the Page and are audited — there is no scope
// check and no deny-list (Phase 2 reintroduces an execution boundary for the account-safety
// deny-list).
type Action struct {
    Kind   ActionKind
    Target Handle
    Point  *Coord
    Value  string
    Meta   map[string]string
}

// Skill is a deterministic, code-first capability tried BEFORE the planner LLM.
type Skill interface {
    Name() string
    CanHandle(s State, goal Goal) bool
    Run(ctx context.Context, p browser.Page, s State, goal Goal, deps SkillDeps) (SkillOutcome, error)
}

// Planner is the LLM-backed fallback (only when no Skill applies / a Skill escalated).
type Planner interface {
    Next(ctx context.Context, in PlanInput) (Plan, error)
}

// Verifier decides whether the last action achieved intent; detects error/stuck/logout. Mostly
// deterministic; escalates to a cheap LLM only for ambiguous "did this work?" judgments.
type Verifier interface {
    Verify(ctx context.Context, before, after State, a Action, goal Goal) (Verdict, error)
}
```

---

## auth — Authenticator, FlowHandler

```go
package auth

// Authenticator is the single owner of obtaining a session using OPERATOR-PROVIDED credentials
// (never generated). It owns login/signup flows, verification (VerificationProvider), CAPTCHA
// and human handoff (captcha subsystem), success detection and session restore. The JobManager
// calls Run; on success it produces the SessionBundle + AuthRecording for handback.
type Authenticator interface {
    // Run authenticates (target, account) in the given mode. Order: (1) restore a healthy saved
    // session without logging in; (2) resolve the entrypoint (hints → per-target cache → discovery,
    // via EntrypointDiscovery); (3) mode=login → login flow; mode=signup → the SIGNUP SEQUENCE:
    // signup → verify (link/code) → log in if the app did not authenticate us after verification.
    // Signup switches to login on "account exists". SSO-only apps fail fast with ErrSSOOnly (no
    // OAuth/SSO flow in v1). Verification/CAPTCHA/human-handoff run internally and may suspend the
    // job as needs_verification / needs_human (resumed via JobManager.ResolveChallenge). On success
    // it runs the post-login capture window + replayability check and returns the handback Result.
    Run(ctx context.Context, target string, account identity.AccountID, mode Mode, hints Hints) (handback.Result, error)
}

// Mode selects login vs signup.
type Mode int
const ( Login Mode = iota; Signup )

// Hints are optional operator-supplied entrypoint URLs passed through start_auth.
type Hints struct {
    LoginURL  string
    SignupURL string
}

// EntrypointDiscovery reaches the login/signup form before any flow runs (§6.0). Resolution order:
// operator hints → per-target cache (invalidated if it no longer yields a recognisable form) →
// deterministic discovery from the root (link/button/a11y names like "sign in"/"register"; common
// paths /login,/signin,/register,/signup,/auth,/account,…; header/nav controls that open a modal; a
// form counts only if it matches a login/signup signature) → cheap LLM fallback over the a11y
// snapshot. The resolved entrypoint (URL + how it was reached) is cached per target and traced.
type EntrypointDiscovery interface {
    Resolve(ctx context.Context, p browser.Page, target string, mode Mode, hints Hints) (Entrypoint, error)
    // Cache/Invalidate manage the per-target cache of discovered entrypoints.
    Cache(ctx context.Context, target string, mode Mode, e Entrypoint) error
    Invalidate(ctx context.Context, target string, mode Mode) error
}

// Entrypoint is where a login/signup form was found and how (e.g. "modal via header button").
type Entrypoint struct {
    URL    string
    How    string // hint | cache | link | path | modal | llm
    Reached time.Time
}

// FlowHandler handles one login/signup variant (form, multistep, modal, magiclink, otp, totp).
// The Authenticator detects the variant and dispatches. The handler OWNS the form submit (so it
// owns CAPTCHA submit + Resolver.Outcome) and OWNS form-error recovery: validation → fix &
// resubmit; signup "account exists" → switch to login; give up after N. deps carries the
// VerificationProvider, the CAPTCHA Resolver/HumanHandoff, and the optional signup Profile.
type FlowHandler interface {
    Variant() FlowVariant
    CanHandle(s core.State) bool
    Run(ctx context.Context, p browser.Page, creds identity.Credentials, deps FlowDeps) (FlowResult, error)
}

// SuccessDetector asserts login succeeded from deterministic signals (new auth cookie/bearer-JWT,
// /me 200, authenticated-only element, login form gone). LLM judgment only breaks ties (§6.3).
type SuccessDetector interface {
    IsAuthenticated(ctx context.Context, p browser.Page, s core.State, before Baseline) (bool, Evidence)
}
```

---

## verify — VerificationProvider

```go
package verify

type Channel string
const (
    Email Channel = "email" // OTP code or magic link
    SMS   Channel = "sms"
    TOTP  Channel = "totp"
    Push  Channel = "push"
)

// Request: which channel the page wants, which account, a hint lifted from the page ("code sent
// to ***1234"), and the time window — accept only a code/link after Since, by Deadline.
type Request struct {
    Channel  Channel
    Account  identity.AccountID
    Hint     string
    Since    time.Time
    Deadline time.Time
}

type ResultKind int
const (
    ResCode     ResultKind = iota // one-time code to type
    ResLink                        // magic link to open
    ResApproved                    // push approval granted out of band
)
type Result struct {
    Kind ResultKind
    Code string
    Link string
}

// VerificationProvider: THE AGENT ASKS, THE OPERATOR'S TOOL ANSWERS. Implementations: built-in
// TOTP (immediate from a seed), CLI prompt (blocking), webhook, tool/MCP (async: the job returns
// needs_verification and resumes on resolve_challenge — ADR 0015).
type VerificationProvider interface {
    Supports(ch Channel, account identity.AccountID) bool
    // GetCode returns the code/link/approval, bounded by ctx/Deadline (ErrTimeout/ErrDeclined).
    GetCode(ctx context.Context, req Request) (Result, error)
    // Resend re-triggers delivery where supported; the caller then GetCode()s with advanced Since.
    Resend(ctx context.Context, req Request) error
}
```

---

## captcha — Detector, Injector, Resolver, Solver (+ proto), HumanHandoff

> Core owns everything that touches the browser (Detector, Injector, HumanHandoff apply); solver
> plugins own ONLY solving and never touch the browser (ADR 0011).

```go
package captcha

// Detector (core, per vendor) recognises a challenge from visible elements AND invisible/score
// signals (network + script signals + failed-submit responses). reCAPTCHA v2/v3, hCaptcha,
// Turnstile, Arkose, custom.
type Detector interface {
    Vendor() Vendor
    Detect(ctx context.Context, p browser.Page, s core.State, net NetSignals) (Challenge, bool, error)
}

// Injector (core, per vendor) applies a solved Result: inject token, perform UIActions, or no-op
// after a human handoff. ONLY core injects.
type Injector interface {
    Vendor() Vendor
    Apply(ctx context.Context, p browser.Page, c Challenge, r Result) error
}

type Capabilities struct {
    Vendors        []Vendor
    Kinds          []ChallengeKind // interactive | score | image | custom
    NeedsImage     bool
    NeedsProxy     bool
    EgressExternal bool
    EstCostUSD     float64
    EstLatency     time.Duration
}

type ResultKind int
const (
    ResultToken     ResultKind = iota
    ResultUIActions
    ResultHandoff
)
type Result struct {
    Kind    ResultKind
    Token   string
    Actions []RegionClick
    SolveID string
}

// Solver is the plugin contract. Built-ins in-process (testmode, vision); external over gRPC.
// Describe() is called ONCE at load so the Resolver can pre-filter its chain.
type Solver interface {
    Name() string
    Describe() Capabilities
    Solve(ctx context.Context, c Challenge) (Result, error)
    Report(ctx context.Context, solveID string, accepted bool) error // forwarded by Resolver.Outcome
}

// Resolver owns a per-target ORDERED chain, PRE-FILTERED at load by cached Describe(). It applies
// each solver's Result via the Injector within Budget; the flow handler owns submit and re-detects
// a FRESH challenge per retry. When the chain is exhausted or the agent is blocked, the flow
// handler escalates to HumanHandoff.
type Resolver interface {
    Resolve(ctx context.Context, p browser.Page, c Challenge) (Result, error)
    Outcome(ctx context.Context, solveID string, accepted bool) error
    // Exhausted reports whether the eligible chain has no more solvers to try for this challenge.
    Exhausted() bool
}

// BlockReason says why the agent handed off — recorded in the trace and surfaced in needs_human.
type BlockReason string
const (
    CaptchaExhausted BlockReason = "captcha_exhausted" // eligible solver chain exhausted
    BlockPage        BlockReason = "block_page"        // block/denied page
    ChallengeLoop    BlockReason = "challenge_loop"    // repeated challenge after accepted solves
    UnknownFlow      BlockReason = "unknown_flow"      // no handler recognises the auth flow
    Stuck            BlockReason = "stuck"             // N steps without progress
)

// HumanHandoff covers ANY dead end, not only CAPTCHA: chain exhausted, block page, challenge loop,
// unrecognised flow, or stuck after N steps. It gives a human live access to the real browser —
// headful (operator at the machine) or a remote DevTools/screencast view — fires a notification,
// and blocks until the operator signals done. Over MCP the JobManager surfaces
// needs_human{challenge_id, view_url, reason}, resolved by ResolveChallenge(id, done).
// SECURITY: a remote view is full control of an authenticated browser. view_url is bound to
// localhost by default; when exposed beyond the host it carries a ONE-TIME token that expires when
// the challenge is resolved or the job ends.
type HumanHandoff interface {
    // Request opens/annotates live access and returns how the operator reaches it (a tokenised view
    // URL for remote DevTools/screencast, empty for local headful) plus the challenge id.
    Request(ctx context.Context, p browser.Page, reason BlockReason, c *Challenge) (challengeID, viewURL string, err error)
    // Wait blocks until the operator signals completion (or ctx/Deadline → ErrHandoffTimeout), then
    // the flow resumes. The one-time token is revoked on return.
    Wait(ctx context.Context, challengeID string) error
}
```

### Solver gRPC plugin — `captcha/plugin/solver.proto`
```proto
syntax = "proto3";
package captcha.plugin.v1;

// A Solver plugin implements SolverService. The host never sends the browser page — only the
// Challenge. The same service backs an in-host go-plugin and a remote solver.
service SolverService {
  rpc Describe (DescribeRequest) returns (Capabilities); // once at load
  rpc Solve    (SolveRequest)    returns (SolveResult);
  rpc Report   (ReportRequest)   returns (ReportAck);    // acceptance feedback for ranking
}
message DescribeRequest {}
message Capabilities {
  repeated string vendors = 1;  // "recaptcha_v2","hcaptcha","turnstile","arkose","custom"
  repeated string kinds   = 2;  // "interactive","score","image","custom"
  bool   needs_image      = 3;
  bool   needs_proxy      = 4;
  bool   egress_external  = 5;
  double est_cost_usd     = 6;
  int64  est_latency_ms   = 7;
}
message Challenge {
  string vendor   = 1;
  string kind     = 2;
  string site_key = 3;
  string page_url = 4;          // origin only; no page contents
  bytes  image    = 5;          // only when needs_image
  map<string,string> params = 6;
}
message SolveRequest { Challenge challenge = 1; string solve_id = 2; }
message SolveResult {
  string solve_id = 1;
  oneof result {
    string token            = 2;
    RegionClicks ui_actions = 3;
    bool   handoff          = 4;
  }
}
message RegionClicks { repeated Region regions = 1; }
message Region { int32 x = 1; int32 y = 2; int32 w = 3; int32 h = 4; }
message ReportRequest { string solve_id = 1; bool accepted = 2; }
message ReportAck {}
```

---

## handback — SessionBundle, AuthRecorder, ReplayabilityCheck, ArtefactWriter

```go
package handback

// Result is what Authenticator.Run returns on success. The bundle and recording are written to the
// per-account workspace and handed back BY REFERENCE (paths for the CLI, MCP resource URIs for MCP)
// — never inline in a job response (they are large and contain secrets).
type Result struct {
    BundleRef    ArtefactRef
    RecordingRef ArtefactRef
    Trace        Trace // entrypoint + how found, flow variant, verification/CAPTCHA/handoff steps (+reason), replayability, timings
}

// ArtefactRef points at a written artefact.
type ArtefactRef struct {
    Path string // local file path (CLI)
    URI  string // MCP resource URI (MCP)
}

// SessionBundle is everything a caller needs to replay the authenticated session elsewhere.
type SessionBundle struct {
    Cookies        []Cookie          // name,value,domain,path,Secure,HttpOnly,SameSite,expiry
    LocalStorage   map[string]string
    SessionStorage map[string]string
    AuthHeaders    map[string]string // Authorization/bearer, CSRF, custom headers seen on post-login XHR/fetch
    UserAgent      string
    Proxy          string            // proxy used, if any (IP binding may matter for reuse)
    TokenExpiry    *time.Time        // parsed JWT exp where available
    Replayable     bool              // §7.4: a plain-HTTP replay of one authed XHR matched the browser
    ReplayReason   string            // when !Replayable: tls_fingerprint | request_signing | dpop | js_computed_headers | ip_bound | …
}

// CaptureWindow bounds the post-login recording: after success detection the agent loads the
// landing page once, then records until N authenticated requests OR T seconds, whichever first.
type CaptureWindow struct {
    N int           // e.g. 20
    T time.Duration // e.g. 10s
}

// AuthRecorder captures the auth flow as HAR-style JSON: every request/response during signup/
// login PLUS a bounded post-login capture window, so a caller can see and replay exactly how auth
// works. Bodies included; secrets flagged. It records the AUTH FLOW ONLY — not a general
// traffic/discovery engine.
type AuthRecorder interface {
    Begin(ctx context.Context, p browser.Page) error                      // subscribe to CDP network events
    MarkAuthenticated(ctx context.Context, w CaptureWindow) error         // start the post-login window
    Finalize(ctx context.Context) (AuthRecording, error)                  // stop + return the recording
}

// AuthRecording is the HAR-style artefact with secrets flagged.
type AuthRecording struct {
    Entries []HAREntry // request+response, bodies included; each field marked Secret where sensitive
    Format  string     // "har-1.2-json"
}

// ReplayChecker answers "can the caller use plain HTTP with this bundle, or must it keep a
// browser?" It picks one captured authenticated XHR (GET preferred, e.g. the /me-style request) and
// replays it with a plain Go HTTP client using ONLY the bundle's cookies + headers. Same status and
// an equivalent body ⇒ replayable; else a reason (TLS/client fingerprinting, request signing, DPoP/
// proof-of-possession, JS-computed per-request headers, IP/proxy-bound session).
type ReplayChecker interface {
    Check(ctx context.Context, b SessionBundle, rec AuthRecording) (replayable bool, reason string, err error)
}

// ArtefactWriter persists the bundle and recording into the per-account workspace and returns
// references (paths / MCP resource URIs). Nothing is returned inline.
type ArtefactWriter interface {
    WriteBundle(ctx context.Context, account identity.AccountID, b SessionBundle) (ArtefactRef, error)
    WriteRecording(ctx context.Context, account identity.AccountID, r AuthRecording) (ArtefactRef, error)
}
```

---

## server — JobManager (CLI + MCP)

```go
package server

// JobState is the polled status of an auth job.
type JobState string
const (
    Running           JobState = "running"
    NeedsVerification JobState = "needs_verification"
    NeedsHuman        JobState = "needs_human"
    Done              JobState = "done"
    Failed            JobState = "failed"
    Cancelled         JobState = "cancelled"
)

// FailReason is the named reason on a failed job — every dead end has an exit (invariant 9).
type FailReason string
const (
    SSOOnly         FailReason = "sso_only"         // app offers SSO only; no native login/signup
    BudgetExhausted FailReason = "budget_exhausted"
    Cancelled       FailReason = "cancelled"
    HandoffTimeout  FailReason = "handoff_timeout"  // needs_human expired unanswered
    VerifyTimeout   FailReason = "verification_timeout"
    FlowFailed      FailReason = "flow_failed"      // gave up after N recovery attempts
)

// JobManager is the single core entry both the CLI and the MCP server wrap. A PER-ACCOUNT LOCK
// ensures one operation drives an account's context at a time. The CLI answers verification/human
// handoff via an interactive prompt; the MCP server exposes these async shapes.
type JobManager interface {
    // StartAuth launches an auth job (login or signup, with optional entrypoint hints) and returns
    // immediately with a job id.
    StartAuth(ctx context.Context, target string, account identity.AccountID, mode auth.Mode, hints auth.Hints) (JobID, error)
    // GetJob returns status and, when finished, the handback Result (artefact REFS + trace); when
    // suspended, the pending challenge (verification or human) to answer.
    GetJob(ctx context.Context, id JobID) (Job, error)
    // ResolveChallenge answers a pending challenge — ONE call for both pending states: a code,
    // link or approval answers needs_verification; "done" answers needs_human.
    ResolveChallenge(ctx context.Context, challengeID string, ans Answer) error
    CancelJob(ctx context.Context, id JobID) error
    // ExportSession returns a reference to the latest SessionBundle for an account.
    ExportSession(ctx context.Context, account identity.AccountID) (handback.ArtefactRef, error)
}

type Job struct {
    ID      JobID
    State   JobState
    Pending *Pending          // set for NeedsVerification / NeedsHuman
    Result  *handback.Result  // set for Done: {BundleRef, RecordingRef, Trace}
    Reason  FailReason        // set for Failed
}

// Pending is surfaced to the harness to answer out of band.
type Pending struct {
    ChallengeID string
    Kind        string              // "verification" | "human"
    Channel     string              // verification: email|sms|totp|push
    Hint        string              // "code sent to ***1234"
    ViewURL     string              // needs_human: localhost-bound or one-time-token URL ("" for local headful)
    Reason      captcha.BlockReason // needs_human: why handoff was triggered
    ExpiresAt   time.Time
}

type Answer struct {
    Code     string // verification code
    Link     string // magic link
    Approved bool   // push approval
    Done     bool   // needs_human: operator finished acting in the live browser
}
```

---

## identity — credential store (supporting)

```go
package identity

// Credentials are OPERATOR-PROVIDED and never generated.
type Credentials struct {
    UsernameOrEmail string
    SecretRef       string // encrypted-store pointer; never cleartext
    TOTPSeedRef     string // optional
    Phone           string // optional, for SMS-OTP routing
}

// Profile holds OPTIONAL operator values for incidental signup fields (name, company, address,
// DOB…). When absent, the signup flow GENERATES sensible values (config flag). Credentials are
// never generated — only these incidental fields.
type Profile struct { Fields map[string]string }

// CredentialStore holds operator credentials + optional profile per account, encrypted at rest
// (key from an env var). Each account uses its own persistent browser profile. No identity generation.
type CredentialStore interface {
    PutCredentials(ctx context.Context, target string, account AccountID, creds Credentials) error
    GetCredentials(ctx context.Context, account AccountID) (Credentials, error)
    PutProfile(ctx context.Context, account AccountID, p Profile) error
    GetProfile(ctx context.Context, account AccountID) (Profile, error) // empty if none
}
```

---

## browser / audit (supporting)

```go
package browser

// Browser mints isolated contexts (one per account). ContextConfig carries a PERSISTENT profile
// dir per account (reduces CAPTCHAs), realistic UA/viewport/timezone, human-like pacing, and an
// optional upstream ProxyURL.
type Browser interface {
    NewContext(ctx context.Context, cfg ContextConfig) (Context, error)
    Close(ctx context.Context) error
}

// Context is one isolated account environment. ExportState/ImportState back session restore and
// the SessionBundle.
type Context interface {
    NewPage(ctx context.Context) (Page, error)
    ExportState(ctx context.Context) (SessionState, error)
    ImportState(ctx context.Context, s SessionState) error
    Close(ctx context.Context) error
}

// Page is a single tab. WaitStable settles SPAs (network-idle + DOM-mutation quiescence + route
// stability). Subscribe streams CDP network events to the AuthRecorder.
type Page interface {
    Navigate(ctx context.Context, url string) error
    URL() string
    Snapshot(ctx context.Context) (Snapshot, error)
    Screenshot(ctx context.Context, opt ShotOpts) ([]byte, error)
    Act(ctx context.Context, a Action) (ActionResult, error)
    WaitStable(ctx context.Context, cond StableCond) error
    Frames() []Frame
    OnPopup(func(Page))
    Subscribe(sink NetworkSink) (cancel func()) // feeds AuthRecorder + auth-header capture
}
```

```go
package audit

// AuditLog is an append-only, hash-chained record of every action, request, LLM decision and auth
// event — redacted BEFORE write (the flow carries live credentials/tokens). A failed append trips
// the kill-switch. The SessionBundle/AuthRecording are never written to the log in clear.
type AuditLog interface {
    Append(ctx context.Context, e Entry) (EntryID, error) // e redacted before persistence
    Redact(e Entry) Entry
    Query(ctx context.Context, f Filter) ([]Entry, error)
}

// Budget enforces limits; Check runs at loop top, before every LLM call, and within the solver
// chain. ResourceKind: Time | Requests | Tokens | CostUSD | SolverCostUSD | Solves.
type Budget interface {
    Check(kind ResourceKind, amount float64) error
    Spend(kind ResourceKind, amount float64)
    Snapshot() BudgetState
}
```
