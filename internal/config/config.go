// Package config loads runtime configuration and lays out the per-account
// workspace on disk.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the runtime configuration. Values come from flags/env; there is
// no mandatory config file.
type Config struct {
	Workspace string        // root dir for accounts, artefacts, audit, cache
	Headless  bool          // run Chrome headless (default true; false for human handoff)
	ProxyURL  string        // optional upstream proxy for the browser
	UserAgent string        // optional UA override
	Timeout   time.Duration // per-job time budget
	Capture   CaptureWindow
	LLM       LLMConfig
	StepLimit int // max agent steps per job before "stuck"
}

// CaptureWindow bounds the post-login recording.
type CaptureWindow struct {
	N int           // stop after N authenticated requests
	T time.Duration // or after T seconds
}

// LLMConfig selects the provider.
type LLMConfig struct {
	Provider string // mock | openai
	BaseURL  string
	Model    string
	JSONMode bool
}

// Default returns a Config with sensible defaults.
func Default() Config {
	ws := os.Getenv("AGENTBROWSER_WORKSPACE")
	if ws == "" {
		ws = ".workspace"
	}
	prov := os.Getenv("AGENTBROWSER_LLM")
	if prov == "" {
		switch {
		case os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("OPENAI_BASE_URL") != "":
			prov = "openai"
		case os.Getenv("ANTHROPIC_API_KEY") != "":
			prov = "anthropic"
		case os.Getenv("GEMINI_API_KEY") != "":
			prov = "gemini"
		default:
			prov = "mock"
		}
	}
	return Config{
		Workspace: ws,
		Headless:  strings.ToLower(os.Getenv("AGENTBROWSER_HEADFUL")) != "1",
		ProxyURL:  os.Getenv("AGENTBROWSER_PROXY"),
		Timeout:   5 * time.Minute,
		Capture:   CaptureWindow{N: 20, T: 10 * time.Second},
		LLM:       LLMConfig{Provider: prov, JSONMode: true},
		StepLimit: 40,
	}
}

// Workspace describes the on-disk layout for one account.
type Workspace struct {
	Root       string
	AccountDir string
	Profile    string // persistent Chrome user-data-dir
	Artefacts  string // bundles + recordings
	AuditPath  string
	SessionEnc string // encrypted saved session
	CacheDir   string // per-target entrypoint cache
}

// ForAccount resolves (and creates) the workspace for an account.
func (c Config) ForAccount(account string) (Workspace, error) {
	acc := sanitize(account)
	root, err := filepath.Abs(c.Workspace)
	if err != nil {
		return Workspace{}, fmt.Errorf("config: workspace: %w", err)
	}
	w := Workspace{
		Root:       root,
		AccountDir: filepath.Join(root, "accounts", acc),
		Profile:    filepath.Join(root, "accounts", acc, "profile"),
		Artefacts:  filepath.Join(root, "accounts", acc, "artefacts"),
		AuditPath:  filepath.Join(root, "accounts", acc, "audit.jsonl"),
		SessionEnc: filepath.Join(root, "accounts", acc, "session.enc"),
		CacheDir:   filepath.Join(root, "cache"),
	}
	for _, d := range []string{w.AccountDir, w.Profile, w.Artefacts, w.CacheDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return Workspace{}, fmt.Errorf("config: mkdir %s: %w", d, err)
		}
	}
	return w, nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}
