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
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/browser"
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

// App holds process-wide dependencies.
type App struct {
	Cfg    config.Config
	Key    *[32]byte
	Creds  identity.CredentialStore
	Router model.ModelRouter
	// CrawlModel, when set, overrides the router's model for the LLM/hybrid crawl
	// driver. Used by tests to script a deterministic action sequence.
	CrawlModel explore.LLMModel
}

// New builds an App from config, reading the store key from the environment.
func New(cfg config.Config) (*App, error) {
	key, err := identity.KeyFromEnv()
	if err != nil {
		return nil, err
	}
	creds, err := identity.NewFileStore(cfg.Workspace, key)
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
		p := provider.NewOpenAICompat(provider.OpenAIOptions{BaseURL: l.BaseURL, Model: l.Model, JSONMode: l.JSONMode})
		return &model.StaticRouter{Default: p}, nil
	case "anthropic":
		p := provider.NewAnthropic(provider.AnthropicOptions{BaseURL: l.BaseURL, Model: l.Model})
		return &model.StaticRouter{Default: p}, nil
	case "gemini":
		p := provider.NewGemini(provider.GeminiOptions{BaseURL: l.BaseURL, Model: l.Model})
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

	b, err := browser.Launch(bctx, browser.Options{Headless: a.Cfg.Headless, ProfileDir: ws.Profile, ProxyURL: a.Cfg.ProxyURL, UserAgent: a.Cfg.UserAgent})
	if err != nil {
		return AuthResult{Err: fmt.Errorf("launch browser: %w", err), Reason: "flow_failed"}
	}
	defer b.Close()

	resolver, handoff := a.captchaKit(asker, al, bud, target)
	authr := &auth.Authenticator{
		Target: target, Account: account, Creds: creds, Profile: profile,
		Perceiver: perceive.New(), Router: a.Router,
		Sessions:   &auth.FileSessionStore{Path: ws.SessionEnc, Key: a.Key},
		Writer:     &handback.FileWriter{Dir: ws.Artefacts, Account: account},
		Audit:      al,
		Budget:     bud,
		Verifier:   a.verifier(account, creds, asker),
		Captcha:    resolver,
		Handoff:    handoff,
		Capture:    handback.CaptureWindow{N: a.Cfg.Capture.N, T: a.Cfg.Capture.T},
		EntryCache: ws.CacheDir,
		Timeout:    8 * time.Second,
	}
	res, err := authr.Run(bctx, b, mode, hints)
	if err != nil {
		return AuthResult{Err: err, Reason: auth.FailReason(err)}
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
