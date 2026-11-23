// Package app wires the concrete components together for the CLI and the MCP
// server: credential store, browser, authenticator, artefact writer, audit log
// and budget, all rooted in a per-account workspace.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/browser/camoufox"     // register "camoufox" backend + its handoff view
	_ "github.com/creativetoken/chromancer/internal/browser/rodbackend" // register "chromium" backend
	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/captcha/plugin"
	"github.com/creativetoken/chromancer/internal/captcha/solver"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/explore"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/model/provider"
	"github.com/creativetoken/chromancer/internal/perceive"
	"github.com/creativetoken/chromancer/internal/verify"
)

// Asker bridges verification and human-handoff prompts to a caller (the MCP
// job surface implements it). A nil Asker means CLI/interactive prompts.
type Asker interface {
	verify.Asker
	captcha.Asker
}

// RecoveryReporter is an optional capability of an Asker: when the LLM agent
// takes over a dead end, progress is reported here so a control surface can
// expose a recovering{step,reason} state. Askers that do not implement it simply
// do not receive the notifications.
type RecoveryReporter interface {
	Recovering(step int, reason string)
}

// App holds process-wide dependencies.
type App struct {
	Cfg    config.Config
	Key    *[32]byte
	Creds  identity.CredentialStore
	Router model.ModelRouter
	// CrawlModel, when set, overrides the router's model for the LLM/hybrid crawl
	// driver. Used by tests to script a deterministic action sequence.
	CrawlModel explore.LLMModel
	// AuthModel, when set, overrides the router for the auth flow (entrypoint LLM
	// fallback, classification). Used by tests to script entry discovery.
	AuthModel model.LLMProvider
	// Recovery selects what happens when the deterministic flow hits a dead end:
	// RecoveryAgent (default) tries the LLM agent (when a model is configured)
	// then a human; RecoveryHuman skips the agent; RecoveryOff fails fast. The
	// default degrades to human-only when no LLM provider is available.
	Recovery auth.RecoveryMode
	// AgentModel, when set, overrides the model handed to the agent takeover.
	// Used by tests to script the takeover's action sequence.
	AgentModel explore.LLMModel
	// AuthDriver selects deterministic (default) or agent-primary sign-in. In
	// agent-primary mode the LLM agent drives the whole flow from the entry page;
	// it requires a model (RunAuth fails fast when none is configured).
	AuthDriver auth.AuthDriver
}

// New builds an App from config, reading the store key from the environment.
func New(cfg config.Config) (*App, error) {
	// Credentials come from a plaintext YAML file the operator edits directly.
	// Encryption is optional: AGENTBROWSER_KEY is read ONLY when set (to open
	// legacy encrypted session/mailbox files, or to re-enable encryption on
	// purpose); otherwise key stays nil and session.enc / mailbox tokens are
	// written in plaintext.
	var key *[32]byte
	if os.Getenv(identity.KeyEnv) != "" {
		k, err := identity.KeyFromEnv()
		if err != nil {
			return nil, err
		}
		key = k
	}
	creds, err := identity.NewYAMLStore(cfg.CredsFile)
	if err != nil {
		return nil, err
	}
	router, err := buildRouter(cfg.LLM)
	if err != nil {
		return nil, err
	}
	return &App{Cfg: cfg, Key: key, Creds: creds, Router: router}, nil
}

func buildRouter(l config.LLMConfig) (model.ModelRouter, error) {
	switch l.Provider {
	case "", "mock":
		// Mock returns {"handle":0} so the LLM entrypoint fallback declines;
		// deterministic discovery handles the lab apps.
		return &model.StaticRouter{Default: model.NewMock(func(string) string { return `{"handle":0,"reason":"mock"}` })}, nil
	case "openai":
		p := provider.NewOpenAICompat(provider.OpenAIOptions{BaseURL: l.BaseURL, Model: l.Model, JSONMode: l.JSONMode, Vision: l.Vision, PriceIn: l.PriceIn, PriceOut: l.PriceOut})
		return &model.StaticRouter{Default: p}, nil
	case "anthropic":
		p := provider.NewAnthropic(provider.AnthropicOptions{BaseURL: l.BaseURL, Model: l.Model, Vision: l.Vision, PriceIn: l.PriceIn, PriceOut: l.PriceOut})
		return &model.StaticRouter{Default: p}, nil
	case "gemini":
		p := provider.NewGemini(provider.GeminiOptions{BaseURL: l.BaseURL, Model: l.Model, Vision: l.Vision, PriceIn: l.PriceIn, PriceOut: l.PriceOut})
		return &model.StaticRouter{Default: p}, nil
	default:
		return nil, fmt.Errorf("app: unknown llm provider %q", l.Provider)
	}
}

// AuthResult is returned by RunAuth.
type AuthResult struct {
	Result handback.Result
	Err    error
	Reason string
}

// RunAuth launches a browser and runs the authenticator for one account. asker
// (may be nil) bridges verification to an MCP pending/answer surface; when nil
// an interactive CLI prompt is used.
func (a *App) RunAuth(ctx context.Context, account string, mode auth.Mode, hints auth.Hints, asker Asker) AuthResult {
	ws, err := a.Cfg.ForAccount(account)
	if err != nil {
		return AuthResult{Err: err, Reason: "flow_failed"}
	}
	creds, err := a.Creds.GetCredentials(ctx, identity.AccountID(account))
	if err != nil {
		return AuthResult{Err: fmt.Errorf("no credentials for account %q (use `creds put`): %w", account, err), Reason: "flow_failed"}
	}
	profile, _ := a.Creds.GetProfile(ctx, identity.AccountID(account))
	target, _ := a.Creds.Target(ctx, identity.AccountID(account))
	if target == "" {
		return AuthResult{Err: fmt.Errorf("account %q has no target", account), Reason: "flow_failed"}
	}

	red := audit.NewRedactor()
	red.AddSecret(creds.Password)
	if creds.TOTPSeed != "" {
		red.AddSecret(creds.TOTPSeed)
	}
	al, err := audit.OpenFile(ws.AuditPath, red)
	if err != nil {
		return AuthResult{Err: err, Reason: "flow_failed"}
	}
	defer al.Close()

	bud := core.NewBudget(core.Limits{Time: a.Cfg.Timeout, Tokens: 200000, Steps: float64(a.Cfg.StepLimit)})
	bctx, cancel := bud.Context(ctx)
	defer cancel()

	backend := a.Cfg.BackendFor(target)
	b, err := browser.Open(bctx, backend, browser.Options{Headless: a.Cfg.Headless, ProfileDir: ws.Profile, ProxyURL: a.Cfg.ProxyURL, UserAgent: a.Cfg.UserAgent, AccountDir: ws.AccountDir})
	if err != nil {
		return AuthResult{Err: fmt.Errorf("launch browser (%s): %w", backend, err), Reason: "flow_failed"}
	}
	defer b.Close()

	resolver, handoff := a.captchaKit(asker, al, bud, target)
	authRouter := a.Router
	if a.AuthModel != nil {
		authRouter = &model.StaticRouter{Default: a.AuthModel}
	}
	// Agent takeover model: an explicit override, else the planning model from
	// the router. When neither exists, the agent is nil and recovery degrades to
	// human-only.
	var agentModel explore.LLMModel
	if a.Recovery != auth.RecoveryOff || a.AuthDriver == auth.AuthDriverAgent {
		switch {
		case a.AgentModel != nil:
			agentModel = a.AgentModel
		case authRouter != nil && authRouter.For(model.TaskPlan) != nil:
			agentModel = authRouter.For(model.TaskPlan)
		}
	}
	// Agent-primary sign-in needs a model; fail fast with a clear message.
	if a.AuthDriver == auth.AuthDriverAgent && agentModel == nil {
		return AuthResult{Err: fmt.Errorf("auth driver 'agent' needs an LLM provider; none configured (set a model API key)"), Reason: "flow_failed"}
	}
	// Visual-mode model: a vision-capable provider so the agent can escalate to a
	// screenshot + set-of-marks when the DOM can't be read (closed shadow root,
	// canvas, cross-origin iframe). Prefer the router's vision task; fall back to
	// the agent model only if it reports Vision. Nil → visual escalation is off.
	var visionModel explore.LLMModel
	if a.AuthDriver == auth.AuthDriverAgent && authRouter != nil {
		if vp := authRouter.For(model.TaskVision); vp != nil && vp.Capabilities().Vision {
			visionModel = vp
		} else if am, ok := agentModel.(model.LLMProvider); ok && am.Capabilities().Vision {
			visionModel = agentModel
		}
	}
	if a.Recovery == auth.RecoveryOff {
		handoff = nil
	}
	// When the browser runs on a virtual display (camoufox/Xvfb), expose that
	// display to a human handoff through the token-guarded noVNC view: the
	// handoff's one-time token gates every request including the WebSocket. The
	// view starts lazily on the first escalation and is torn down with the job.
	if handoff != nil {
		if caps := b.Capabilities(); caps.Display != "" {
			handoff.Headful = false
			var (
				once sync.Once
				view *camoufox.VNCView
				base string
				verr error
			)
			handoff.ViewProvider = func(token, challenge string) (string, error) {
				once.Do(func() {
					view = &camoufox.VNCView{Display: caps.Display, Valid: handoff.ValidToken}
					base, verr = view.Start(bctx)
				})
				if verr != nil {
					return "", verr
				}
				return view.ViewURL(base, token), nil
			}
			defer func() {
				if view != nil {
					view.Close()
				}
			}()
		}
	}
	authr := &auth.Authenticator{
		Target: target, Account: account, Creds: creds, Profile: profile,
		Perceiver: perceive.New(), Router: authRouter,
		Sessions:      &auth.FileSessionStore{Path: ws.SessionEnc, Key: a.Key},
		Writer:        &handback.FileWriter{Dir: ws.Artefacts, Account: account},
		Audit:         al,
		Budget:        bud,
		Verifier:      a.verifier(account, creds, asker),
		Captcha:       resolver,
		Handoff:       handoff,
		Agent:         agentModel,
		VisionAgent:   visionModel,
		Recovery:      a.Recovery,
		AuthDriver:    a.AuthDriver,
		Capture:       handback.CaptureWindow{N: a.Cfg.Capture.N, T: a.Cfg.Capture.T},
		EntryCache:    ws.CacheDir,
		Timeout:       8 * time.Second,
		VerifyTimeout: a.Cfg.VerifyTimeout,
	}
	if rr, ok := asker.(RecoveryReporter); ok && rr != nil {
		authr.OnRecovering = rr.Recovering
	}
	res, err := authr.Run(bctx, b, mode, hints)
	if err != nil {
		// res carries the partial trace (agent_takeover steps) on failure.
		return AuthResult{Result: res, Err: err, Reason: auth.FailReason(err)}
	}
	return AuthResult{Result: res}
}

// verifier builds the verification chain for an account: built-in TOTP (from
// the stored seed) first, then either the MCP ask-bridge (when asker != nil) or
// an interactive CLI prompt, plus a webhook fallback.
func (a *App) verifier(account string, creds identity.Credentials, asker verify.Asker) verify.Provider {
	var chain verify.Chain
	// Automatic providers first (Twilio SMS, Gmail IMAP, per-target TOTP) when a
	// resources config is set; they only claim channels/accounts they have a
	// resource for, so unconfigured channels fall through to the stored seed and
	// then the ask/CLI fallbacks.
	if path := os.Getenv("AGENTBROWSER_RESOURCES"); path != "" {
		if res, err := verify.LoadResources(path); err == nil {
			login := &LoginHint{Account: account, Email: creds.UsernameOrEmail}
			chain = append(chain, a.BuildVerifyChain(res, verify.NewClaimStore(), login))
		}
	}
	chain = append(chain, verify.NewTOTP(func(acc string) string {
		if acc == account {
			return creds.TOTPSeed
		}
		return ""
	}))
	if asker != nil {
		chain = append(chain, verify.NewAsk(asker))
	} else {
		chain = append(chain, verify.NewCLI(os.Stdin, os.Stderr))
	}
	return chain
}

// captchaKit builds the CAPTCHA resolver (testmode + optional external gRPC/
// subprocess plugin + vision) and the human-handoff escalation. The testmode
// solver answers only the operator's own lab sitekeys.
func (a *App) captchaKit(asker Asker, al audit.AuditLog, bud core.Budget, target string) (captcha.Resolver, *captcha.HumanHandoff) {
	// Chain order and limits come from an optional YAML config; the default
	// chain is testmode → native → vision → external → human (human always last).
	cfg, _ := captcha.LoadChainConfig(os.Getenv("AGENTBROWSER_CAPTCHA_CONFIG"))

	var solvers []captcha.Solver
	if cfg.Has("testmode") {
		solvers = append(solvers, solver.NewTestMode(
			map[string]string{"lab-sitekey": labToken, "block-sitekey": labToken, "": labToken},
			captcha.VendorMock, captcha.VendorRecaptchaV2, captcha.VendorRecaptchaV3, captcha.VendorHCaptcha, captcha.VendorTurnstile,
		))
	}
	if cfg.Has("native") {
		solvers = append(solvers, solver.NewNative())
	}
	if cfg.Has("vision") && a.Router != nil {
		solvers = append(solvers, solver.NewVision(a.Router))
	}
	if cfg.Has("external") {
		for _, ex := range cfg.External {
			if ex.Path != "" {
				solvers = append(solvers, plugin.NewClient(ex.Path))
			}
		}
		if path := strings.TrimSpace(os.Getenv("AGENTBROWSER_SOLVER_PLUGIN")); path != "" {
			solvers = append(solvers, plugin.NewClient(path))
		}
	}

	stats, _ := captcha.LoadStats(filepath.Join(a.Cfg.Workspace, "captcha-stats.json"))
	opts := []captcha.ResolverOption{
		captcha.WithStats(stats, target),
		captcha.WithAdaptiveOrder(cfg.AdaptiveOrder),
		captcha.WithClientHints(a.Cfg.UserAgent, a.Cfg.ProxyURL),
		captcha.WithSolverTimeout(cfg.TimeoutPerSolver),
	}
	resolver := captcha.NewResolver(solvers, nil, captcha.NewCoreBudget(bud), cfg.MaxSolves, al, opts...)

	// Human handoff is always the final fallback, regardless of chain config.
	var handoffAsker captcha.Asker = asker
	if handoffAsker == nil {
		handoffAsker = cliHandoff{}
	}
	h := captcha.NewHumanHandoff(handoffAsker)
	h.Headful = !a.Cfg.Headless
	return resolver, h
}

// labToken is the operator's own lab CAPTCHA test token (not a real solver).
const labToken = "lab-captcha-ok"

// cliHandoff prompts the operator to solve a challenge in the (headful) browser.
type cliHandoff struct{}

// AskHuman implements captcha.Asker for the CLI: it waits for the operator to
// resolve the challenge in the browser and press Enter.
func (cliHandoff) AskHuman(ctx context.Context, req captcha.HandoffRequest) error {
	fmt.Fprintf(os.Stderr, "[human handoff] %s — solve the challenge in the browser", req.Reason)
	if req.ViewURL != "" {
		fmt.Fprintf(os.Stderr, " (%s)", req.ViewURL)
	}
	fmt.Fprint(os.Stderr, ", then press Enter: ")
	done := make(chan struct{})
	go func() {
		var line string
		_, _ = fmt.Fscanln(os.Stdin, &line)
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return captcha.ErrHandoffTimeout
	}
}

// ReadArtefactURI resolves an agentbrowser://artefacts/<account>/<file> URI to
// its bytes, scoped to that account's artefacts directory.
func (a *App) ReadArtefactURI(uri string) ([]byte, string, error) {
	const prefix = "agentbrowser://artefacts/"
	if !strings.HasPrefix(uri, prefix) {
		return nil, "", fmt.Errorf("unsupported uri scheme: %s", uri)
	}
	rest := strings.TrimPrefix(uri, prefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, "", fmt.Errorf("malformed artefact uri: %s", uri)
	}
	account, file := parts[0], parts[1]
	if strings.ContainsAny(file, "/\\") || strings.Contains(file, "..") {
		return nil, "", fmt.Errorf("invalid artefact name")
	}
	ws, err := a.Cfg.ForAccount(account)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(filepath.Join(ws.Artefacts, file))
	if err != nil {
		return nil, "", fmt.Errorf("read artefact: %w", err)
	}
	return data, "application/json", nil
}

// ExportSession returns the latest bundle reference for an account.
func (a *App) ExportSession(ctx context.Context, account string) (handback.ArtefactRef, handback.SessionBundle, error) {
	ws, err := a.Cfg.ForAccount(account)
	if err != nil {
		return handback.ArtefactRef{}, handback.SessionBundle{}, err
	}
	w := &handback.FileWriter{Dir: ws.Artefacts, Account: account}
	ref, err := w.LatestBundle(ctx)
	if err != nil {
		return handback.ArtefactRef{}, handback.SessionBundle{}, err
	}
	b, err := w.ReadBundle(ctx, ref)
	return ref, b, err
}
