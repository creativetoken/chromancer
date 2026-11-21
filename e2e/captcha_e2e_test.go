package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/captcha/plugin"
	"github.com/creativetoken/chromancer/internal/captcha/solver"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/lab"
	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/perceive"
)

func (h *harness) runCaptcha(t *testing.T, app string, solvers []captcha.Solver, handoff *captcha.HumanHandoff) (handback.Result, error) {
	t.Helper()
	e := h.expect(app)
	ws := filepath.Join(h.ws, app)
	var key [32]byte
	key[0] = byte(len(app) + 7)
	al := audit.NewNop()
	bud := core.NewBudget(core.Limits{Time: 120 * time.Second, Solves: 10})
	resolver := captcha.NewResolver(solvers, nil, captcha.NewCoreBudget(bud), 2, al)
	authr := &auth.Authenticator{
		Target: e.Root, Account: app, Creds: identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password},
		Perceiver: perceive.New(),
		Sessions:  &auth.FileSessionStore{Path: filepath.Join(ws, "s.enc"), Key: &key},
		Writer:    &handback.FileWriter{Dir: filepath.Join(ws, "art"), Account: app},
		Audit:     al, Budget: bud, Captcha: resolver, Handoff: handoff,
		Capture: handback.CaptureWindow{N: 3, T: 5 * time.Second}, EntryCache: filepath.Join(ws, "cache"), Timeout: 8 * time.Second,
	}
	return authr.Run(h.ctx, h.b, auth.Login, auth.Hints{LoginURL: e.LoginURL})
}

// TestE2EAgentPrimaryCaptchaMock drives captcha-mock entirely with the agent
// (lab policy): it fills the login form, calls solve_captcha, submits, and
// confirms via check_logged_in.
func TestE2EAgentPrimaryCaptchaMock(t *testing.T) {
	h := newHarness(t)
	e := h.expect("captcha-mock")
	ws := filepath.Join(h.ws, "captcha-mock-agent")
	var key [32]byte
	key[0] = 23
	al := audit.NewNop()
	bud := core.NewBudget(core.Limits{Time: 150 * time.Second, Solves: 10})
	ts := solver.NewTestMode(map[string]string{"lab-sitekey": lab.LabCaptchaToken, "": lab.LabCaptchaToken})
	resolver := captcha.NewResolver([]captcha.Solver{ts}, nil, captcha.NewCoreBudget(bud), 2, al)
	policy := lab.NewPolicyModel()
	authr := &auth.Authenticator{
		Target: e.Root, Account: "captcha-mock", Creds: identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password},
		Perceiver: perceive.New(),
		Sessions:  &auth.FileSessionStore{Path: filepath.Join(ws, "s.enc"), Key: &key},
		Writer:    &handback.FileWriter{Dir: filepath.Join(ws, "art"), Account: "captcha-mock"},
		Audit:     al, Budget: bud, Captcha: resolver,
		Agent: policy, AuthDriver: auth.AuthDriverAgent, Recovery: auth.RecoveryAgent,
		Capture: handback.CaptureWindow{N: 3, T: 5 * time.Second}, EntryCache: filepath.Join(ws, "cache"), Timeout: 8 * time.Second,
	}
	res, err := authr.Run(h.ctx, h.b, auth.Login, auth.Hints{LoginURL: e.LoginURL})
	if err != nil {
		t.Fatalf("agent-primary captcha-mock should succeed: %v (trace %+v)", err, res.Trace)
	}
	if len(res.Trace.SuccessEvidence) == 0 {
		t.Errorf("expected success evidence: %+v", res.Trace)
	}
	if len(res.Trace.Captcha) == 0 {
		t.Errorf("expected a captcha step in the trace: %+v", res.Trace)
	}
}

func TestE2ECaptchaMockTestmode(t *testing.T) {
	h := newHarness(t)
	ts := solver.NewTestMode(map[string]string{"lab-sitekey": lab.LabCaptchaToken, "": lab.LabCaptchaToken})
	res, err := h.runCaptcha(t, "captcha-mock", []captcha.Solver{ts}, nil)
	if err != nil {
		t.Fatalf("captcha-mock via testmode: %v", err)
	}
	if len(res.Trace.Captcha) == 0 {
		t.Fatalf("expected a captcha step in trace: %+v", res.Trace)
	}
	if ts.Reports() == 0 {
		t.Fatal("expected an Outcome report to the solver")
	}
}

func TestE2ECaptchaMockGRPCPlugin(t *testing.T) {
	h := newHarness(t)
	// Build the mock external solver binary and drive it as a subprocess plugin.
	dir := t.TempDir()
	bin := filepath.Join(dir, "mocksolver")
	build := exec.Command("go", "build", "-o", bin, "github.com/creativetoken/chromancer/cmd/mocksolver")
	build.Env = append(os.Environ(), "MOCKSOLVER_TOKEN="+lab.LabCaptchaToken)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mocksolver: %v\n%s", err, out)
	}
	t.Setenv("MOCKSOLVER_TOKEN", lab.LabCaptchaToken)
	cl := plugin.NewClient(bin)
	defer cl.Close()
	if _, err := h.runCaptcha(t, "captcha-mock", []captcha.Solver{cl}, nil); err != nil {
		t.Fatalf("captcha-mock via gRPC-style plugin: %v", err)
	}
}

// humanSolver is a test HumanHandoff.Asker that solves captcha-block server-side
// (as the operator would in the live browser) then signals done.
type humanSolver struct {
	t      *testing.T
	base   string
	called int
}

func (s *humanSolver) AskHuman(ctx context.Context, req captcha.HandoffRequest) error {
	s.called++
	httpGet(s.t, s.base+"/captcha-block/__human-solve")
	return nil
}

func TestE2ECaptchaBlockHumanHandoff(t *testing.T) {
	h := newHarness(t)
	ts := solver.NewTestMode(map[string]string{"block-sitekey": lab.LabCaptchaToken, "": lab.LabCaptchaToken})
	hs := &humanSolver{t: t, base: h.ts.URL}
	handoff := captcha.NewHumanHandoff(hs)
	res, err := h.runCaptcha(t, "captcha-block", []captcha.Solver{ts}, handoff)
	if err != nil {
		t.Fatalf("captcha-block should succeed after human handoff: %v", err)
	}
	if hs.called == 0 {
		t.Fatal("human handoff was never invoked")
	}
	if res.Trace.HumanHandoff == "" {
		t.Fatalf("trace should record the handoff reason: %+v", res.Trace)
	}
}

// rerenderSolver POSTs the captcha-rerender solve endpoint, like a human
// solving the challenge in the browser.
type rerenderSolver struct {
	t    *testing.T
	base string
}

func (s *rerenderSolver) AskHuman(ctx context.Context, req captcha.HandoffRequest) error {
	httpGet(s.t, s.base+"/captcha-rerender/__human-solve")
	// As a real operator's solve would, let the page observe the solve and
	// re-render (swap the submit button) before we return — so the handle
	// perceived before the hook is now stale.
	time.Sleep(500 * time.Millisecond)
	return nil
}

// TestE2ECaptchaRerenderStaleHandle reproduces the live failure where solving
// the CAPTCHA re-rendered the form and the submit handle perceived before the
// hook went stale ("submit: element … context deadline exceeded"). The handler
// must re-perceive after the CAPTCHA hook and submit the fresh control; the run
// should log in rather than fail on a stale handle.
func TestE2ECaptchaRerenderStaleHandle(t *testing.T) {
	h := newHarness(t)
	ts := solver.NewTestMode(map[string]string{"block-sitekey": lab.LabCaptchaToken, "": lab.LabCaptchaToken})
	handoff := captcha.NewHumanHandoff(&rerenderSolver{t: t, base: h.ts.URL})
	res, err := h.runCaptcha(t, "captcha-rerender", []captcha.Solver{ts}, handoff)
	if err != nil {
		t.Fatalf("captcha-rerender should log in despite the submit button being replaced: %v", err)
	}
	if res.Trace.HumanHandoff == "" {
		t.Fatalf("expected the run to go through a human handoff: %+v", res.Trace)
	}
	if len(res.Trace.SuccessEvidence) == 0 {
		t.Fatalf("expected authentication success evidence: %+v", res.Trace)
	}
}

func TestE2ECaptchaBlockHandoffTimeout(t *testing.T) {
	h := newHarness(t)
	ts := solver.NewTestMode(map[string]string{"": lab.LabCaptchaToken})
	// An asker that never completes → the handoff deadline fires.
	handoff := captcha.NewHumanHandoff(stuckAsker{})
	_, err := h.runCaptchaDL(t, "captcha-block", []captcha.Solver{ts}, handoff, 6*time.Second)
	if auth.FailReason(err) != "handoff_timeout" {
		t.Fatalf("expected handoff_timeout, got %v (reason %q)", err, auth.FailReason(err))
	}
}

type stuckAsker struct{}

func (stuckAsker) AskHuman(ctx context.Context, req captcha.HandoffRequest) error {
	<-ctx.Done()
	return captcha.ErrHandoffTimeout
}

// runCaptchaDL is runCaptcha with a short handoff deadline via a budget-bound ctx.
func (h *harness) runCaptchaDL(t *testing.T, app string, solvers []captcha.Solver, handoff *captcha.HumanHandoff, dl time.Duration) (handback.Result, error) {
	t.Helper()
	e := h.expect(app)
	ws := filepath.Join(h.ws, app+"-dl")
	var key [32]byte
	al := audit.NewNop()
	bud := core.NewBudget(core.Limits{Time: dl, Solves: 10})
	bctx, cancel := bud.Context(h.ctx)
	defer cancel()
	resolver := captcha.NewResolver(solvers, nil, captcha.NewCoreBudget(bud), 2, al)
	authr := &auth.Authenticator{
		Target: e.Root, Account: app, Creds: identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password},
		Perceiver: perceive.New(), Sessions: &auth.FileSessionStore{Path: filepath.Join(ws, "s.enc"), Key: &key},
		Writer: &handback.FileWriter{Dir: filepath.Join(ws, "art"), Account: app},
		Audit:  al, Budget: bud, Captcha: resolver, Handoff: handoff,
		Capture: handback.CaptureWindow{N: 2, T: 3 * time.Second}, EntryCache: filepath.Join(ws, "cache"), Timeout: 5 * time.Second,
	}
	return authr.Run(bctx, h.b, auth.Login, auth.Hints{LoginURL: e.LoginURL})
}

// TestE2EStuckEscalatesToHandoff: a login that never progresses escalates to a
// human; after the operator solves it server-side, the flow completes.
func TestE2EStuckEscalatesToHandoff(t *testing.T) {
	h := newHarness(t)
	hs := &humanSolver{t: t, base: h.ts.URL}
	// Reuse humanSolver but point it at the stuck-login solve endpoint.
	solveHandoff := captcha.NewHumanHandoff(&stuckSolver{t: t, base: h.ts.URL})
	_ = hs
	res, err := h.runCaptcha(t, "stuck-login", nil, solveHandoff)
	if err != nil {
		t.Fatalf("stuck-login should succeed after handoff: %v", err)
	}
	if res.Trace.HumanHandoff == "" {
		t.Fatalf("expected a handoff in the trace: %+v", res.Trace)
	}
}

type stuckSolver struct {
	t    *testing.T
	base string
}

func (s *stuckSolver) AskHuman(ctx context.Context, req captcha.HandoffRequest) error {
	httpGet(s.t, s.base+"/stuck-login/__human-solve")
	return nil
}

var _ = model.TaskVision
