// Package captcha detects CAPTCHA challenges (core, per vendor), resolves them
// through a Describe()-prefiltered chain of solver plugins within a budget, and
// applies the typed result to the page (core Injector). Solvers only solve and
// never touch the browser; the flow handler owns the form submit.
package captcha

import (
	"context"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// Vendor identifies a CAPTCHA provider.
type Vendor string

// Known vendors.
const (
	VendorMock        Vendor = "mock"
	VendorRecaptchaV2 Vendor = "recaptcha_v2"
	VendorRecaptchaV3 Vendor = "recaptcha_v3"
	VendorHCaptcha    Vendor = "hcaptcha"
	VendorTurnstile   Vendor = "turnstile"
	VendorArkose      Vendor = "arkose"
	VendorCustom      Vendor = "custom"
)

// Kind is the challenge interaction model.
type Kind string

// Challenge kinds.
const (
	KindInteractive Kind = "interactive"
	KindScore       Kind = "score"
	KindImage       Kind = "image"
	KindCustom      Kind = "custom"
)

// Challenge is a detected CAPTCHA (no page contents leave the host — only the
// vendor, kind, sitekey, page origin and vendor params).
type Challenge struct {
	Vendor   Vendor            `json:"vendor"`
	Kind     Kind              `json:"kind"`
	SiteKey  string            `json:"site_key"`
	PageURL  string            `json:"page_url"`
	Image    []byte            `json:"image,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	fieldSel string            // internal: the response-field selector the injector targets
}

// ResultKind discriminates the typed solve result.
type ResultKind int

// Result kinds.
const (
	ResultToken     ResultKind = iota // a verification token to inject
	ResultUIActions                   // clicks on set-of-marks regions
	ResultHandoff                     // a human solved it live; core does not inject
	ResultText                        // text to type into a text-captcha input
)

// RegionClick is a click target for ResultUIActions.
type RegionClick struct {
	X, Y, W, H int
}

// Result is what a Solver returns; the core Injector applies it.
type Result struct {
	Kind    ResultKind    `json:"kind"`
	Token   string        `json:"token,omitempty"`
	Text    string        `json:"text,omitempty"` // text-captcha answer (ResultText)
	Actions []RegionClick `json:"actions,omitempty"`
	SolveID string        `json:"solve_id"`
}

// Payload returns the verification value a token/text result carries (for the
// acceptance check that it reached the backend). Empty for UIActions/Handoff.
func (r Result) Payload() string {
	switch r.Kind {
	case ResultToken:
		return r.Token
	case ResultText:
		return r.Text
	default:
		return ""
	}
}

// Capabilities is what a Solver advertises (cached from Describe at load).
type Capabilities struct {
	Vendors        []Vendor `json:"vendors"`
	Kinds          []Kind   `json:"kinds"`
	NeedsImage     bool     `json:"needs_image"`
	NeedsProxy     bool     `json:"needs_proxy"`
	EgressExternal bool     `json:"egress_external"`
	EstCostUSD     float64  `json:"est_cost_usd"`
	EstLatencyMS   int64    `json:"est_latency_ms"`
}

// Supports reports whether these capabilities cover a vendor/kind.
func (c Capabilities) Supports(v Vendor, k Kind) bool {
	vOK := len(c.Vendors) == 0
	for _, x := range c.Vendors {
		if x == v || x == VendorCustom {
			vOK = true
		}
	}
	kOK := len(c.Kinds) == 0 || k == ""
	for _, x := range c.Kinds {
		if x == k {
			kOK = true
		}
	}
	return vOK && kOK
}

// NetSignals carries out-of-DOM detection hints (script srcs seen on the page,
// and the status of the last form submit).
type NetSignals struct {
	ScriptSrcs       []string
	FailedSubmit     bool
	FailedSubmitBody string
}

// Detector (core, per vendor) recognises a challenge from the page.
type Detector interface {
	Vendor() Vendor
	Detect(ctx context.Context, p *browser.Page, st perceive.State, sig NetSignals) (Challenge, bool, error)
}

// Injector (core, per vendor) applies a solved Result to the live page.
type Injector interface {
	Vendor() Vendor
	Apply(ctx context.Context, p *browser.Page, c Challenge, r Result) error
}

// Solver is the plugin contract: solving only, never the Page. Describe() is
// called once at load.
type Solver interface {
	Name() string
	Describe() Capabilities
	Solve(ctx context.Context, c Challenge) (Result, error)
	Report(ctx context.Context, solveID string, accepted bool) error
}

// Resolver runs a prefiltered chain of solvers within budget and applies each
// result via the Injector. The flow handler owns submit and calls Outcome.
type Resolver interface {
	Resolve(ctx context.Context, p *browser.Page, c Challenge) (Result, error)
	Outcome(ctx context.Context, solveID string, accepted bool) error
	Exhausted(c Challenge) bool
}

// Budget caps solver spend; implemented by core.Budget via an adapter.
type Budget interface {
	Check(solves, costUSD float64) error
	Spend(solves, costUSD float64)
}

// Sentinel errors.
type solverError string

func (e solverError) Error() string { return string(e) }

// Errors.
const (
	ErrNoSolver    = solverError("no eligible solver for challenge")
	ErrExhausted   = solverError("solver chain exhausted")
	ErrDeclined    = solverError("solver declined")
	ErrUnsupported = solverError("challenge vendor unsupported")
)

var _ = time.Second
