package config

import (
	"os"
	"testing"
	"time"
)

func TestVisionDefault(t *testing.T) {
	t.Setenv("AGENTBROWSER_LLM_VISION", "") // no override
	// Real hosted providers are vision-capable by default; mock is not.
	for _, prov := range []string{"openai", "anthropic", "gemini"} {
		if !visionDefault(prov) {
			t.Errorf("provider %q should default to vision-capable", prov)
		}
	}
	for _, prov := range []string{"mock", ""} {
		if visionDefault(prov) {
			t.Errorf("provider %q should not be vision-capable by default", prov)
		}
	}
}

func TestVisionDefaultEnvOverride(t *testing.T) {
	// Force on for the mock provider.
	t.Setenv("AGENTBROWSER_LLM_VISION", "1")
	if !visionDefault("mock") {
		t.Error("AGENTBROWSER_LLM_VISION=1 should force vision on")
	}
	// Force off for a real provider.
	t.Setenv("AGENTBROWSER_LLM_VISION", "false")
	if visionDefault("anthropic") {
		t.Error("AGENTBROWSER_LLM_VISION=false should force vision off")
	}
}

func TestDefaultLLMVisionWiredFromProvider(t *testing.T) {
	// A real provider key (and no override) → Default() enables vision so the
	// vision CAPTCHA solver engages.
	t.Setenv("AGENTBROWSER_LLM", "")
	t.Setenv("AGENTBROWSER_LLM_VISION", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "x")
	cfg := Default()
	if cfg.LLM.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", cfg.LLM.Provider)
	}
	if !cfg.LLM.Vision {
		t.Error("LLM.Vision should be true for an anthropic provider")
	}
}

func TestBackendForPrecedence(t *testing.T) {
	cfg := Config{
		Backend:         "chromium",
		BackendByTarget: map[string]string{"shop.example.com": "camoufox"},
	}
	cases := []struct {
		target, want string
	}{
		{"https://shop.example.com/uk/login", "camoufox"}, // per-target override wins
		{"shop.example.com", "camoufox"},                  // bare host
		{"https://SHOP.example.com/", "camoufox"},         // case-insensitive host
		{"https://other.example.com/login", "chromium"},   // falls back to global
		{"", "chromium"}, // global default
	}
	for _, c := range cases {
		if got := cfg.BackendFor(c.target); got != c.want {
			t.Errorf("BackendFor(%q) = %q, want %q", c.target, got, c.want)
		}
	}

	// No global set anywhere → defaults to chromium.
	empty := Config{}
	if got := empty.BackendFor("https://x.example.com"); got != "chromium" {
		t.Errorf("empty config BackendFor = %q, want chromium", got)
	}
}

func TestDefaultBackendFromEnv(t *testing.T) {
	t.Setenv("AGENTBROWSER_BACKEND", "camoufox")
	t.Setenv("AGENTBROWSER_BACKEND_TARGETS", "a.example.com=chromium, b.example.com=camoufox")
	cfg := Default()
	if cfg.Backend != "camoufox" {
		t.Errorf("Backend = %q, want camoufox", cfg.Backend)
	}
	if cfg.BackendFor("https://a.example.com/x") != "chromium" {
		t.Errorf("per-target override for a.example.com not applied")
	}
	if cfg.BackendFor("https://c.example.com/x") != "camoufox" {
		t.Errorf("global backend not applied for unlisted host")
	}
}

func TestEnvDuration(t *testing.T) {
	const def = 5 * time.Minute
	cases := []struct {
		set  bool
		val  string
		want time.Duration
	}{
		{false, "", def}, // unset → default
		{true, "", def},  // empty → default
		{true, "12m", 12 * time.Minute},
		{true, "90s", 90 * time.Second},
		{true, "garbage", def}, // unparseable → default
		{true, "-3m", def},     // negative → default
		{true, "0s", 0},        // explicit zero is honored
	}
	for _, c := range cases {
		if c.set {
			t.Setenv("AGENTBROWSER_TEST_DUR", c.val)
		} else {
			os.Unsetenv("AGENTBROWSER_TEST_DUR")
		}
		if got := envDuration("AGENTBROWSER_TEST_DUR", def); got != c.want {
			t.Errorf("envDuration(set=%v, %q) = %v, want %v", c.set, c.val, got, c.want)
		}
	}
}

func TestVerifyTimeoutFromEnv(t *testing.T) {
	t.Setenv("AGENTBROWSER_VERIFY_TIMEOUT", "12m")
	t.Setenv("AGENTBROWSER_TIMEOUT", "14m")
	cfg := Default()
	if cfg.VerifyTimeout != 12*time.Minute {
		t.Errorf("VerifyTimeout = %v, want 12m", cfg.VerifyTimeout)
	}
	if cfg.Timeout != 14*time.Minute {
		t.Errorf("Timeout = %v, want 14m", cfg.Timeout)
	}
}

func TestVerifyTimeoutDefaultsZero(t *testing.T) {
	os.Unsetenv("AGENTBROWSER_VERIFY_TIMEOUT")
	if cfg := Default(); cfg.VerifyTimeout != 0 {
		t.Errorf("VerifyTimeout default = %v, want 0 (use authenticator default)", cfg.VerifyTimeout)
	}
}
