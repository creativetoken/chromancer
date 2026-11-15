package captcha

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/creativetoken/chromancer/internal/browser"
)

func TestCapabilitiesSupports(t *testing.T) {
	c := Capabilities{Vendors: []Vendor{VendorRecaptchaV2}, Kinds: []Kind{KindInteractive}}
	if !c.Supports(VendorRecaptchaV2, KindInteractive) {
		t.Fatal("should support")
	}
	if c.Supports(VendorHCaptcha, KindInteractive) {
		t.Fatal("wrong vendor supported")
	}
	// Custom vendor in caps is a wildcard.
	cc := Capabilities{Vendors: []Vendor{VendorCustom}}
	if !cc.Supports(VendorTurnstile, KindInteractive) {
		t.Fatal("custom should wildcard vendors")
	}
}

func TestSiteKeyFromRender(t *testing.T) {
	if got := siteKeyFromRender("https://www.google.com/recaptcha/api.js?render=6LdABC_xyz"); got != "6LdABC_xyz" {
		t.Fatalf("sitekey: %q", got)
	}
	if got := siteKeyFromRender("https://x/recaptcha/api.js?render=explicit&foo=1"); got != "" {
		t.Fatalf("explicit should be empty: %q", got)
	}
}

// fakeSolver is a scripted in-process solver.
type fakeSolver struct {
	name    string
	caps    Capabilities
	result  Result
	err     error
	reports []bool
	mu      sync.Mutex
}

func (f *fakeSolver) Name() string           { return f.name }
func (f *fakeSolver) Describe() Capabilities { return f.caps }
func (f *fakeSolver) Solve(_ context.Context, c Challenge) (Result, error) {
	return f.result, f.err
}
func (f *fakeSolver) Report(_ context.Context, _ string, ok bool) error {
	f.mu.Lock()
	f.reports = append(f.reports, ok)
	f.mu.Unlock()
	return nil
}

// recordInjector records applied results instead of touching a page.
type recordInjector struct {
	vendor  Vendor
	applied []Result
}

func (r *recordInjector) Vendor() Vendor { return r.vendor }
func (r *recordInjector) Apply(_ context.Context, _ *browser.Page, _ Challenge, res Result) error {
	r.applied = append(r.applied, res)
	return nil
}

func TestResolverChainAndOutcome(t *testing.T) {
	s1 := &fakeSolver{name: "a", caps: Capabilities{Vendors: []Vendor{VendorMock}, Kinds: []Kind{KindInteractive}, EstCostUSD: 0}, err: ErrDeclined}
	s2 := &fakeSolver{name: "b", caps: Capabilities{Vendors: []Vendor{VendorMock}, Kinds: []Kind{KindInteractive}, EstCostUSD: 0.5}, result: Result{Kind: ResultToken, Token: "tok"}}
	inj := &recordInjector{vendor: VendorMock}
	r := NewResolver([]Solver{s2, s1}, map[Vendor]Injector{VendorMock: inj}, nil, 3, nil)
	ch := Challenge{Vendor: VendorMock, Kind: KindInteractive, SiteKey: "sk"}

	// First Resolve → cheapest eligible solver is s1 (cost 0) which declines.
	if _, err := r.Resolve(context.Background(), nil, ch); !errors.Is(err, ErrDeclined) {
		t.Fatalf("first resolve should decline via s1: %v", err)
	}
	// Second Resolve → next solver s2 returns a token, injected.
	res, err := r.Resolve(context.Background(), nil, ch)
	if err != nil || res.Token != "tok" || res.SolveID == "" {
		t.Fatalf("second resolve: %+v %v", res, err)
	}
	if len(inj.applied) != 1 || inj.applied[0].Token != "tok" {
		t.Fatalf("injector not applied: %+v", inj.applied)
	}
	// Chain now exhausted.
	if !r.Exhausted(ch) {
		t.Fatal("chain should be exhausted after both solvers tried")
	}
	// Outcome routes to the right solver's Report.
	if err := r.Outcome(context.Background(), res.SolveID, true); err != nil {
		t.Fatal(err)
	}
	if len(s2.reports) != 1 || !s2.reports[0] {
		t.Fatalf("s2 report not recorded: %+v", s2.reports)
	}
}

func TestResolverNoEligibleSolver(t *testing.T) {
	s := &fakeSolver{name: "h", caps: Capabilities{Vendors: []Vendor{VendorHCaptcha}}}
	r := NewResolver([]Solver{s}, nil, nil, 3, nil)
	if _, err := r.Resolve(context.Background(), nil, Challenge{Vendor: VendorTurnstile}); !errors.Is(err, ErrNoSolver) {
		t.Fatalf("want ErrNoSolver, got %v", err)
	}
}
