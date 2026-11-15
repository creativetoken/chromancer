package captcha

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadChainConfigDefaults(t *testing.T) {
	cfg, err := LoadChainConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxSolves != 5 || !cfg.Has("testmode") || !cfg.Has("human") || cfg.AdaptiveOrder {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadChainConfigYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	yaml := `
captcha:
  chain: [testmode, external, human]
  max_solves: 3
  max_cost: 0.25
  timeout_per_solver: 45s
  adaptive_order: true
  external:
    - name: generic
      path: ./plugins/captcha-generic
      endpoint: https://solver.example
      api_key_env: SOLVER_API_KEY
      vendors: [recaptcha_v2, hcaptcha]
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadChainConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxSolves != 3 || cfg.MaxCostUSD != 0.25 || cfg.TimeoutPerSolver != 45*time.Second || !cfg.AdaptiveOrder {
		t.Fatalf("scalars: %+v", cfg)
	}
	if cfg.Has("native") || cfg.Has("vision") {
		t.Fatal("native/vision should be disabled by this chain")
	}
	if !cfg.Has("external") || len(cfg.External) != 1 || cfg.External[0].Path != "./plugins/captcha-generic" {
		t.Fatalf("external: %+v", cfg.External)
	}
	if len(cfg.External[0].Vendors) != 2 {
		t.Fatalf("external vendors: %+v", cfg.External[0].Vendors)
	}
}
