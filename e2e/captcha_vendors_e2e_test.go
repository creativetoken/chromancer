package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/captcha/solver"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// vendorTestmode is a testmode solver that answers the vendors' published test
// sitekeys (and the lab tokens).
func vendorTestmode() *solver.TestMode {
	return solver.NewTestMode(
		map[string]string{"lab-sitekey": "lab-captcha-ok", "block-sitekey": "lab-captcha-ok", "": "lab-captcha-ok"},
		captcha.VendorMock, captcha.VendorRecaptchaV2, captcha.VendorRecaptchaV3, captcha.VendorHCaptcha, captcha.VendorTurnstile,
	)
}

func TestE2EVendorTestmode(t *testing.T) {
	for _, app := range []string{"mock-recaptcha-v2", "mock-hcaptcha"} {
		app := app
		t.Run(app, func(t *testing.T) {
			h := newHarness(t)
			res, err := h.runCaptcha(t, app, []captcha.Solver{vendorTestmode()}, nil)
			if err != nil {
				t.Fatalf("%s via testmode: %v", app, err)
			}
			if len(res.Trace.Captcha) == 0 {
				t.Fatalf("%s: expected a captcha step: %+v", app, res.Trace)
			}
		})
	}
}

func TestE2ENativeTurnstileManaged(t *testing.T) {
	h := newHarness(t)
	// Only native in the chain: the managed widget auto-fills; testmode would
	// decline the non-test sitekey. Proves native solves it.
	res, err := h.runCaptcha(t, "mock-turnstile-managed", []captcha.Solver{solver.NewNative()}, nil)
	if err != nil {
		t.Fatalf("mock-turnstile-managed via native: %v", err)
	}
	if len(res.Trace.Captcha) == 0 {
		t.Fatalf("expected a captcha step: %+v", res.Trace)
	}
}

func TestE2EScoreNonBlocking(t *testing.T) {
	// Invisible/score challenges attach their token on submit: they must NOT
	// block or hand off, even with only a (declining) testmode solver present.
	for _, app := range []string{"mock-recaptcha-invisible", "mock-recaptcha-v3", "mock-turnstile-invisible"} {
		app := app
		t.Run(app, func(t *testing.T) {
			h := newHarness(t)
			if _, err := h.runCaptcha(t, app, []captcha.Solver{vendorTestmode()}, nil); err != nil {
				t.Fatalf("%s (score, non-blocking): %v", app, err)
			}
		})
	}
}

func TestE2ECaptchaHardHandoff(t *testing.T) {
	h := newHarness(t)
	hs := &vendorHumanSolver{base: h.ts.URL, path: "/captcha-hard/__human-solve"}
	handoff := captcha.NewHumanHandoff(hs)
	res, err := h.runCaptcha(t, "captcha-hard", []captcha.Solver{vendorTestmode(), solver.NewNative()}, handoff)
	if err != nil {
		t.Fatalf("captcha-hard should succeed after handoff: %v", err)
	}
	if hs.called == 0 {
		t.Fatal("human handoff never invoked for captcha-hard")
	}
	if res.Trace.HumanHandoff == "" {
		t.Fatalf("expected handoff in trace: %+v", res.Trace)
	}
}

func TestE2ECaptchaOnFailedSubmit(t *testing.T) {
	h := newHarness(t)
	if _, err := h.runCaptcha(t, "captcha-on-failed-submit", []captcha.Solver{vendorTestmode()}, nil); err != nil {
		t.Fatalf("captcha-on-failed-submit: %v", err)
	}
}

func TestE2EVisionTextCaptcha(t *testing.T) {
	h := newHarness(t)
	v := solver.NewVision(&model.StaticRouter{Default: &labVision{base: h.ts.URL}})
	if _, err := h.runCaptcha(t, "text-captcha", []captcha.Solver{v}, nil); err != nil {
		t.Fatalf("text-captcha via vision: %v", err)
	}
}

func TestE2EVisionImageGrid(t *testing.T) {
	h := newHarness(t)
	v := solver.NewVision(&model.StaticRouter{Default: &labVision{base: h.ts.URL}})
	if _, err := h.runCaptcha(t, "image-grid", []captcha.Solver{v}, nil); err != nil {
		t.Fatalf("image-grid via vision: %v", err)
	}
}

func TestE2ECaptchaBudgetExhausted(t *testing.T) {
	h := newHarness(t)
	// captcha-block accepts the injected token then always re-challenges. Two
	// solvers both succeed-but-get-rejected; the 1-solve budget refuses the
	// second Resolve before the chain exhausts → failed{budget_exhausted}.
	block := func() captcha.Solver {
		return solver.NewTestMode(map[string]string{"block-sitekey": "lab-captcha-ok", "": "lab-captcha-ok"})
	}
	_, err := h.runCaptchaBudget(t, "captcha-block", []captcha.Solver{block(), block()}, 1)
	if auth.FailReason(err) != "budget_exhausted" {
		t.Fatalf("expected budget_exhausted, got %v (reason %q)", err, auth.FailReason(err))
	}
}

// runCaptchaBudget is runCaptcha with an explicit Solves budget and no handoff.
func (h *harness) runCaptchaBudget(t *testing.T, app string, solvers []captcha.Solver, solves float64) (handback.Result, error) {
	t.Helper()
	e := h.expect(app)
	ws := filepath.Join(h.ws, app+"-bud")
	var key [32]byte
	key[0] = 9
	al := audit.NewNop()
	bud := core.NewBudget(core.Limits{Time: 120 * time.Second, Solves: solves})
	resolver := captcha.NewResolver(solvers, nil, captcha.NewCoreBudget(bud), 5, al)
	authr := &auth.Authenticator{
		Target: e.Root, Account: app, Creds: identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password},
		Perceiver: perceive.New(),
		Sessions:  &auth.FileSessionStore{Path: filepath.Join(ws, "s.enc"), Key: &key},
		Writer:    &handback.FileWriter{Dir: filepath.Join(ws, "art"), Account: app},
		Audit:     al, Budget: bud, Captcha: resolver,
		Capture: handback.CaptureWindow{N: 2, T: 3 * time.Second}, EntryCache: filepath.Join(ws, "cache"), Timeout: 8 * time.Second,
	}
	return authr.Run(h.ctx, h.b, auth.Login, auth.Hints{LoginURL: e.LoginURL})
}

// vendorHumanSolver solves a lab app's challenge server-side (as the operator
// would in the live browser) then signals done.
type vendorHumanSolver struct {
	base, path string
	called     int
}

func (s *vendorHumanSolver) AskHuman(ctx context.Context, req captcha.HandoffRequest) error {
	s.called++
	// httpGet needs *testing.T; use a bare client instead.
	_ = ctx
	resp, err := httpClient.Get(s.base + s.path)
	if err == nil {
		_ = resp.Body.Close()
	}
	return nil
}

// labVision is a vision-capable provider that returns the lab's ground truth
// (fetched from each app's /__lab/truth endpoint) — proving the detect→vision→
// inject→submit pipeline without a real model.
type labVision struct{ base string }

func (labVision) Name() string                     { return "lab-vision" }
func (labVision) Capabilities() model.Capabilities { return model.Capabilities{Vision: true} }
func (labVision) Chat(context.Context, []model.Message, model.ChatOpts) (*model.Completion, error) {
	return &model.Completion{}, nil
}

func (p *labVision) Structured(_ context.Context, msgs []model.Message, _ json.RawMessage, _ model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	prompt := ""
	for _, m := range msgs {
		prompt += m.Text
	}
	if strings.Contains(prompt, "grid") {
		var truth struct {
			Correct []int                 `json:"correct"`
			Tiles   []captcha.RegionClick `json:"tiles"`
		}
		_ = json.Unmarshal(httpGetNoT(p.base+"/image-grid/__lab/truth"), &truth)
		var regions []captcha.RegionClick
		for _, i := range truth.Correct {
			if i >= 0 && i < len(truth.Tiles) {
				regions = append(regions, truth.Tiles[i])
			}
		}
		out, _ := json.Marshal(map[string]any{"regions": regions, "confidence": 1.0})
		return out, &model.Usage{}, nil
	}
	var truth struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(httpGetNoT(p.base+"/text-captcha/__lab/truth"), &truth)
	out, _ := json.Marshal(map[string]any{"text": truth.Code, "confidence": 1.0})
	return out, &model.Usage{}, nil
}
