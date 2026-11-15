// Command mocksolver is a mock OUT-OF-PROCESS CAPTCHA solver plugin used in
// tests and demos. It speaks the stdio-JSON solver protocol and returns a
// configured token for the lab mock vendor. It defeats no real CAPTCHA.
package main

import (
	"context"
	"os"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/captcha/plugin"
)

type mockSolver struct{ token string }

func (m mockSolver) Name() string { return "mock-external" }

func (m mockSolver) Describe() captcha.Capabilities {
	return captcha.Capabilities{
		Vendors:        []captcha.Vendor{captcha.VendorMock},
		Kinds:          []captcha.Kind{captcha.KindInteractive},
		EgressExternal: false,
		EstCostUSD:     0,
		EstLatencyMS:   5,
	}
}

func (m mockSolver) Solve(_ context.Context, c captcha.Challenge) (captcha.Result, error) {
	if c.Vendor != captcha.VendorMock {
		return captcha.Result{}, captcha.ErrUnsupported
	}
	return captcha.Result{Kind: captcha.ResultToken, Token: m.token}, nil
}

func (m mockSolver) Report(context.Context, string, bool) error { return nil }

func main() {
	token := os.Getenv("MOCKSOLVER_TOKEN")
	if token == "" {
		token = "lab-captcha-ok"
	}
	_ = plugin.Serve(mockSolver{token: token}, os.Stdin, os.Stdout)
}
