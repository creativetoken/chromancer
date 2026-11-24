// Package config loads runtime configuration and lays out the per-account
// workspace on disk.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

	// VerifyTimeout bounds how long a single verification fetch (doVerify)
	// waits for a code/link. 0 means "use the authenticator default" (90s).
	// Raise it (AGENTBROWSER_VERIFY_TIMEOUT) when the mailbox relay is slow:
	// e.g. the HackerOne mail-forwarder can take many minutes to forward a
	// Bambu Lab OTP, far longer than the 90s default poll window. A value
	// here is capped by the overall per-job Timeout, so raise both together.
	VerifyTimeout time.Duration

	// Backend selects the browser engine ("chromium" | "camoufox"), default
	// "chromium". BackendByTarget pins a specific target host to a backend,
	// overriding Backend for that host (host matched case-insensitively).
	Backend         string
	BackendByTarget map[string]string

	// CredsFile is the plaintext YAML file holding per-account credentials
	// (AGENTBROWSER_CREDS; default <Workspace>/accounts.yaml).
	CredsFile string
}

// BackendFor returns the backend to use for a target URL/host: a per-target
// override if one matches the host, else the global Backend, else "chromium".
func (c Config) BackendFor(target string) string {
	host := hostOf(target)
	if host != "" && len(c.BackendByTarget) > 0 {
		if b := c.BackendByTarget[strings.ToLower(host)]; b != "" {
			return b
		}
	}
	if c.Backend != "" {
		return c.Backend
	}
	return "chromium"
}

// hostOf extracts the lowercase host[:port] from a URL or bare host string.
func hostOf(target string) string {
	s := target
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if j := strings.IndexAny(s, "/?#"); j >= 0 {
		s = s[:j]
	}
	return strings.ToLower(s)
}

// parseBackendTargets parses "host=backend,host2=backend2" into a map.
func parseBackendTargets(s string) map[string]string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) != 2 {
			continue
		}
		host, backend := strings.ToLower(strings.TrimSpace(kv[0])), strings.TrimSpace(kv[1])
		if host != "" && backend != "" {
			out[host] = backend
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CaptureWindow bounds the post-login recording.
type CaptureWindow struct {
	N int           // stop after N authenticated requests
	T time.Duration // or after T seconds
}

// LLMConfig selects the provider.
type LLMConfig struct {
	Provider string // mock | openai | anthropic | gemini
	BaseURL  string
	Model    string
	JSONMode bool
	Vision   bool    // provider's model can read images (enables the vision CAPTCHA solver)
	PriceIn  float64 // USD per 1M input tokens (0 = unknown; enables cost accounting)
	PriceOut float64 // USD per 1M output tokens
}

// visionDefault reports whether a provider's hosted models are vision-capable by
// default. The real hosted providers (Claude 3+, GPT-4o+, Gemini 1.5+) all read
// images; the mock does not. An explicit AGENTBROWSER_LLM_VISION ("1"/"0",
// "true"/"false") overrides this.
func visionDefault(provider string) bool {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("AGENTBROWSER_LLM_VISION"))); v != "" {
		return v == "1" || v == "true" || v == "yes"
	}
	switch provider {
	case "openai", "anthropic", "gemini":
		return true
	default:
		return false
	}
}

// Default returns a Config with sensible defaults.
func Default() Config {
	ws := os.Getenv("AGENTBROWSER_WORKSPACE")
	if ws == "" {
		ws = ".workspace"
	}
	credsFile := os.Getenv("AGENTBROWSER_CREDS")
	if credsFile == "" {
		credsFile = filepath.Join(ws, "accounts.yaml")
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
		Workspace:       ws,
		Headless:        strings.ToLower(os.Getenv("AGENTBROWSER_HEADFUL")) != "1",
		ProxyURL:        os.Getenv("AGENTBROWSER_PROXY"),
		UserAgent:       os.Getenv("AGENTBROWSER_UA"), // optional; empty = real Chrome UA, headless marker stripped
		Timeout:         envDuration("AGENTBROWSER_TIMEOUT", 5*time.Minute),
		VerifyTimeout:   envDuration("AGENTBROWSER_VERIFY_TIMEOUT", 0),
		Capture:         CaptureWindow{N: 20, T: 10 * time.Second},
		LLM:             LLMConfig{Provider: prov, JSONMode: true, Vision: visionDefault(prov), PriceIn: envFloat("AGENTBROWSER_LLM_PRICE_IN"), PriceOut: envFloat("AGENTBROWSER_LLM_PRICE_OUT")},
		StepLimit:       40,
		Backend:         backendDefault(),
		BackendByTarget: parseBackendTargets(os.Getenv("AGENTBROWSER_BACKEND_TARGETS")),
		CredsFile:       credsFile,
	}
}

// envDuration reads a Go duration env var (e.g. "12m", "90s"), returning def
// when the var is unset or does not parse.
func envDuration(name string, def time.Duration) time.Duration {
	s := strings.TrimSpace(os.Getenv(name))
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return def
	}
	return d
}

// envFloat reads a float env var (USD-per-1M-token price), 0 if unset/invalid.
func envFloat(name string) float64 {
	s := strings.TrimSpace(os.Getenv(name))
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// backendDefault reads AGENTBROWSER_BACKEND (default "chromium").
func backendDefault() string {
	b := strings.ToLower(strings.TrimSpace(os.Getenv("AGENTBROWSER_BACKEND")))
	if b == "" {
		return "chromium"
	}
	return b
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
