package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/captcha/plugin"
)

// main reads the adapter config from the environment and serves the solver over
// the host's stdio-JSON plugin protocol. No real service is ever contacted in
// tests; the endpoint/key come from the operator's environment.
//
//	SOLVER_ENDPOINT     base URL of the token-solving service (required)
//	SOLVER_API_KEY_ENV  name of the env var holding the API key (default SOLVER_API_KEY)
//	SOLVER_VENDORS      comma list (default: recaptcha_v2,recaptcha_v3,hcaptcha,turnstile)
//	SOLVER_TIMEOUT      e.g. 120s     SOLVER_POLL_EVERY  e.g. 3s
//	SOLVER_PRICE_USD    e.g. 0.002    SOLVER_NEEDS_PROXY true|false
func main() {
	keyEnv := envOr("SOLVER_API_KEY_ENV", "SOLVER_API_KEY")
	cfg := Config{
		Endpoint:   os.Getenv("SOLVER_ENDPOINT"),
		ClientKey:  os.Getenv(keyEnv),
		Vendors:    parseVendors(envOr("SOLVER_VENDORS", "recaptcha_v2,recaptcha_v3,hcaptcha,turnstile")),
		Timeout:    parseDur(os.Getenv("SOLVER_TIMEOUT"), 120*time.Second),
		PollEvery:  parseDur(os.Getenv("SOLVER_POLL_EVERY"), 3*time.Second),
		PriceUSD:   parseFloat(os.Getenv("SOLVER_PRICE_USD"), 0.002),
		NeedsProxy: os.Getenv("SOLVER_NEEDS_PROXY") == "true",
	}
	_ = plugin.Serve(NewGeneric(cfg), os.Stdin, os.Stdout)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseVendors(s string) []captcha.Vendor {
	var out []captcha.Vendor
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, captcha.Vendor(f))
		}
	}
	return out
}

func parseDur(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return def
}

func parseFloat(s string, def float64) float64 {
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return def
}
