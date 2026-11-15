package solver

import (
	"context"
	"time"

	"github.com/creativetoken/chromancer/internal/captcha"
)

// Native lets the real browser complete a challenge itself. Many invisible /
// managed challenges (Turnstile managed/non-interactive, low-risk reCAPTCHA v2
// checkbox) resolve on their own in a real browser with a persistent profile,
// sometimes after a single checkbox click. It returns UIActions (the core
// clicks the widget via real input events) and the resolver then waits for the
// response field to fill. Free; often sufficient; declines nothing up front —
// if the field never fills the chain advances to the next solver.
type Native struct{}

// NewNative builds a Native solver.
func NewNative() *Native { return &Native{} }

// Name implements captcha.Solver.
func (*Native) Name() string { return "native" }

// Describe implements captcha.Solver.
func (*Native) Describe() captcha.Capabilities {
	return captcha.Capabilities{
		Vendors:      []captcha.Vendor{captcha.VendorRecaptchaV2, captcha.VendorTurnstile, captcha.VendorHCaptcha},
		Kinds:        []captcha.Kind{captcha.KindInteractive},
		EstCostUSD:   0,
		EstLatencyMS: 2500,
	}
}

// Solve implements captcha.Solver. It returns a click on the widget's checkbox
// (so the browser runs its own verification); for a widget with no bounds it
// returns a no-op click and relies on the page auto-filling the token.
func (*Native) Solve(_ context.Context, c captcha.Challenge) (captcha.Result, error) {
	id := c.Params[captcha.ParamSolveID]
	if c.Rect == nil || c.Rect.W == 0 || c.Rect.H == 0 {
		// No checkbox to click (managed/invisible widgets are non-blocking and
		// handled before the chain); nothing for native to do here.
		return captcha.Result{}, captcha.ErrDeclined
	}
	// The reCAPTCHA/Turnstile checkbox sits at the left of the widget. Click a
	// point there via a 1px region (the injector clicks its centre).
	cx := c.Rect.X + min(30, c.Rect.W/2)
	cy := c.Rect.Y + c.Rect.H/2
	return captcha.Result{Kind: captcha.ResultUIActions, Actions: []captcha.RegionClick{{X: cx, Y: cy, W: 1, H: 1}}, SolveID: id}, nil
}

// Report implements captcha.Solver.
func (*Native) Report(context.Context, string, bool) error { return nil }

var _ = time.Second
