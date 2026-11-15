// Package solver holds in-process CAPTCHA solvers: testmode (vendor-published
// test keys + the operator's own lab tokens), native (let the real browser
// complete the challenge), and vision (set-of-marks reasoning via the model
// router). External solvers run out-of-process (internal/captcha/plugin).
package solver

import (
	"context"

	"github.com/creativetoken/chromancer/internal/captcha"
)

// Vendor-published TEST sitekeys and their documented always-pass response
// tokens. These are the vendors' own keys for automated testing (from their
// public docs); they defeat nothing real — the backend must be configured with
// the matching test secret for the token to validate.
var vendorTestTokens = map[string]string{
	// reCAPTCHA v2 — Google's automated-testing key (always passes). The token
	// is arbitrary with the test secret; we send a documented placeholder.
	"6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI": "test-recaptcha-token",
	// hCaptcha — always-pass test sitekey → fixed always-pass token.
	"10000000-ffff-ffff-ffff-000000000001": "10000000-aaaa-bbbb-cccc-000000000001",
	// Cloudflare Turnstile — "always passes" test sitekey (visible/managed/invis).
	"1x00000000000000000000AA": "XXXX.DUMMY.TOKEN.XXXX",
	"1x00000000000000000000BB": "XXXX.DUMMY.TOKEN.XXXX",
	"2x00000000000000000000AB": "XXXX.DUMMY.TOKEN.XXXX",
	"3x00000000000000000000FF": "XXXX.DUMMY.TOKEN.XXXX",
}

// TestMode answers known TEST sitekeys with the vendor's documented test token,
// and the operator's own lab sitekeys from an explicit table. It ships no
// challenge-defeating logic for real keys: a real sitekey yields ErrDeclined.
type TestMode struct {
	// Tokens maps sitekey -> token; the empty-string key is a wildcard default
	// (used by the lab). Vendor test keys are always answered regardless.
	Tokens map[string]string
	// Vendors it will claim (default: mock).
	Vendors []captcha.Vendor
	reports int
}

// NewTestMode returns a TestMode solver. With no vendors it answers the lab mock
// widget; pass vendors to also answer their published test keys.
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

// Solve implements captcha.Solver. It answers a known vendor test sitekey, then
// the operator's configured lab tokens; otherwise it declines.
func (t *TestMode) Solve(_ context.Context, c captcha.Challenge) (captcha.Result, error) {
	id := c.Params[captcha.ParamSolveID]
	// Vendor-published test keys are answered for any claimed vendor.
	if tok, ok := vendorTestTokens[c.SiteKey]; ok && t.claims(c.Vendor) {
		return captcha.Result{Kind: captcha.ResultToken, Token: tok, SolveID: id}, nil
	}
	if !t.claims(c.Vendor) {
		return captcha.Result{}, captcha.ErrUnsupported
	}
	if tok, ok := t.Tokens[c.SiteKey]; ok {
		return captcha.Result{Kind: captcha.ResultToken, Token: tok, SolveID: id}, nil
	}
	// The wildcard default is the lab token: apply it only to the mock vendor so
	// claiming real vendors (for their test keys) never returns a junk token for
	// a real sitekey.
	if tok, ok := t.Tokens[""]; ok && c.Vendor == captcha.VendorMock {
		return captcha.Result{Kind: captcha.ResultToken, Token: tok, SolveID: id}, nil
	}
	return captcha.Result{}, captcha.ErrDeclined
}

func (t *TestMode) claims(v captcha.Vendor) bool {
	for _, x := range t.Vendors {
		if x == v || x == captcha.VendorCustom {
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

// IsVendorTestKey reports whether a sitekey is a vendor-published test key.
func IsVendorTestKey(sitekey string) bool {
	_, ok := vendorTestTokens[sitekey]
	return ok
}
