// Package solver holds in-process CAPTCHA solvers: testmode (known lab tokens)
// and vision (set-of-marks reasoning via the model router). External solvers
// run out-of-process (see internal/captcha/plugin).
package solver

import (
	"context"

	"github.com/creativetoken/chromancer/internal/captcha"
)

// TestMode returns a configured token for known test sitekeys (the operator's
// own benchmark lab). It ships no challenge-defeating logic for real vendors.
type TestMode struct {
	// Tokens maps sitekey -> token; the empty-string key is a wildcard default.
	Tokens map[string]string
	// Vendors it will claim (default: mock).
	Vendors []captcha.Vendor
	reports int
}

// New returns a TestMode solver that answers the lab's mock widget.
func NewTestMode(tokens map[string]string, vendors ...captcha.Vendor) *TestMode {
	if len(vendors) == 0 {
		vendors = []captcha.Vendor{captcha.VendorMock}
	}
	return &TestMode{Tokens: tokens, Vendors: vendors}
}

// Name implements captcha.Solver.
func (t *TestMode) Name() string { return "testmode" }

// Describe implements captcha.Solver.
func (t *TestMode) Describe() captcha.Capabilities {
	return captcha.Capabilities{
		Vendors:    t.Vendors,
		Kinds:      []captcha.Kind{captcha.KindInteractive, captcha.KindScore},
		EstCostUSD: 0, EstLatencyMS: 1,
	}
}

// Solve implements captcha.Solver. It only answers vendors it declared.
func (t *TestMode) Solve(_ context.Context, c captcha.Challenge) (captcha.Result, error) {
	if !t.claims(c.Vendor) {
		return captcha.Result{}, captcha.ErrUnsupported
	}
	id := c.Params["solve_id"]
	if tok, ok := t.Tokens[c.SiteKey]; ok {
		return captcha.Result{Kind: captcha.ResultToken, Token: tok, SolveID: id}, nil
	}
	if tok, ok := t.Tokens[""]; ok {
		return captcha.Result{Kind: captcha.ResultToken, Token: tok, SolveID: id}, nil
	}
	return captcha.Result{}, captcha.ErrDeclined
}

func (t *TestMode) claims(v captcha.Vendor) bool {
	for _, x := range t.Vendors {
		if x == v {
			return true
		}
	}
	return false
}

// Report implements captcha.Solver.
func (t *TestMode) Report(_ context.Context, _ string, _ bool) error {
	t.reports++
	return nil
}

// Reports returns how many Outcome reports were received (test aid).
func (t *TestMode) Reports() int { return t.reports }
