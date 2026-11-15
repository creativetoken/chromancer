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
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/model/provider"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// App holds process-wide dependencies.
type App struct {
	Cfg    config.Config
	Key    *[32]byte
	Creds  identity.CredentialStore
	Router model.ModelRouter
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

// RunAuth launches a browser and runs the authenticator for one account.
func (a *App) RunAuth(ctx context.Context, account string, mode auth.Mode, hints auth.Hints) AuthResult {
	ws, err := a.Cfg.ForAccount(account)
	if err != nil {
		return AuthResult{Err: err, Reason: "flow_failed"}
	}
	creds, err := a.Creds.GetCredentials(ctx, identity.AccountID(account))
	if err != nil {
		return AuthResult{Err: fmt.Errorf("no credentials for account %q (use `creds put`): %w", account, err), Reason: "flow_failed"}
	}
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

	authr := &auth.Authenticator{
		Target: target, Account: account, Creds: creds,
		Perceiver: perceive.New(), Router: a.Router,
		Sessions:   &auth.FileSessionStore{Path: ws.SessionEnc, Key: a.Key},
		Writer:     &handback.FileWriter{Dir: ws.Artefacts, Account: account},
		Audit:      al,
		Budget:     bud,
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
