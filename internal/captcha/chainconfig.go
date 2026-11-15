package captcha

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ExternalConfig configures one out-of-process solver plugin.
type ExternalConfig struct {
	Name       string   `yaml:"name"`
	Path       string   `yaml:"path"`        // plugin binary
	Endpoint   string   `yaml:"endpoint"`    // service base URL (passed via env)
	APIKeyEnv  string   `yaml:"api_key_env"` // env var holding the API key
	Vendors    []string `yaml:"vendors"`
	NeedsProxy bool     `yaml:"needs_proxy"`
	PriceUSD   float64  `yaml:"price_usd"`
}

// ChainConfig is the per-target CAPTCHA chain configuration.
type ChainConfig struct {
	Chain            []string         `yaml:"chain"`              // ordered solver categories
	MaxSolves        int              `yaml:"max_solves"`         // cap on solve attempts per challenge
	MaxCostUSD       float64          `yaml:"max_cost"`           // cap on solver spend
	TimeoutPerSolver time.Duration    `yaml:"timeout_per_solver"` // per-solver timeout
	AdaptiveOrder    bool             `yaml:"adaptive_order"`     // reorder by observed success rate
	External         []ExternalConfig `yaml:"external"`
}

// wire mirrors ChainConfig with a string duration (yaml-friendly).
type wire struct {
	Captcha struct {
		Chain            []string         `yaml:"chain"`
		MaxSolves        int              `yaml:"max_solves"`
		MaxCostUSD       float64          `yaml:"max_cost"`
		TimeoutPerSolver string           `yaml:"timeout_per_solver"`
		AdaptiveOrder    bool             `yaml:"adaptive_order"`
		External         []ExternalConfig `yaml:"external"`
	} `yaml:"captcha"`
}

// DefaultChainConfig is the built-in chain: every automated solver first, human
// last.
func DefaultChainConfig() ChainConfig {
	return ChainConfig{
		Chain:            []string{"testmode", "native", "vision", "external", "human"},
		MaxSolves:        5,
		MaxCostUSD:       0.50,
		TimeoutPerSolver: 90 * time.Second,
		AdaptiveOrder:    false,
	}
}

// LoadChainConfig reads a chain config from a YAML file, filling defaults for
// anything omitted. A missing path returns the default config.
func LoadChainConfig(path string) (ChainConfig, error) {
	cfg := DefaultChainConfig()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	var w wire
	if err := yaml.Unmarshal(data, &w); err != nil {
		return cfg, err
	}
	c := w.Captcha
	if len(c.Chain) > 0 {
		cfg.Chain = c.Chain
	}
	if c.MaxSolves > 0 {
		cfg.MaxSolves = c.MaxSolves
	}
	if c.MaxCostUSD > 0 {
		cfg.MaxCostUSD = c.MaxCostUSD
	}
	if c.TimeoutPerSolver != "" {
		if d, err := time.ParseDuration(c.TimeoutPerSolver); err == nil {
			cfg.TimeoutPerSolver = d
		}
	}
	cfg.AdaptiveOrder = c.AdaptiveOrder
	cfg.External = c.External
	return cfg, nil
}

// Has reports whether a solver category is in the chain (default true when the
// chain is empty, so an unconfigured chain enables everything).
func (c ChainConfig) Has(category string) bool {
	if len(c.Chain) == 0 {
		return true
	}
	for _, x := range c.Chain {
		if x == category {
			return true
		}
	}
	return false
}
