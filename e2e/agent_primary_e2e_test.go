package e2e

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/lab"
	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/perceive"
	"github.com/creativetoken/chromancer/internal/verify"
)

// runAgentPrimary drives the Authenticator in AGENT-PRIMARY mode (the LLM agent
// is the sole driver, no deterministic entry-finder/handlers/resolve loop)
// against a lab app, using the deterministic lab policy as the model. It returns
// the result and error without failing the test so negative cases can assert.
func (h *harness) runAgentPrimary(t *testing.T, app string, creds identity.Credentials, policy *lab.PolicyModel, v verify.Provider) (handback.Result, error) {
	return h.runAgentPrimaryMode(t, app, auth.Login, creds, policy, v)
}

func (h *harness) runAgentPrimaryMode(t *testing.T, app string, mode auth.Mode, creds identity.Credentials, policy *lab.PolicyModel, v verify.Provider) (handback.Result, error) {
	t.Helper()
	e := h.expect(app)
	ws := filepath.Join(h.ws, app)
	var key [32]byte
	key[0] = byte(len(app) + 11)
	red := audit.NewRedactor()
	red.AddSecret(creds.Password)
	al, err := audit.OpenFile(filepath.Join(ws, "audit.jsonl"), red)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	router := &model.StaticRouter{Default: model.NewMock(func(string) string { return `{"handle":0,"reason":"mock"}` })}
	authr := &auth.Authenticator{
		Target: e.Root, Account: app, Creds: creds,
		Perceiver: perceive.New(), Router: router,
		Sessions: &auth.FileSessionStore{Path: filepath.Join(ws, "session.enc"), Key: &key},
		Writer:   &handback.FileWriter{Dir: filepath.Join(ws, "artefacts"), Account: app},
		Audit:    al, Budget: core.NewBudget(core.Limits{Time: 180 * time.Second}),
		Verifier:   v,
		Agent:      policy,
		AuthDriver: auth.AuthDriverAgent,
		Recovery:   auth.RecoveryAgent,
		Handoff:    nil, // no human: a dead end must fail, not block
		Capture:    handback.CaptureWindow{N: 3, T: 5 * time.Second},
		EntryCache: filepath.Join(ws, "cache"), Timeout: 8 * time.Second,
	}
	return authr.Run(h.ctx, h.b, mode, auth.Hints{LoginURL: e.LoginURL, SignupURL: e.SignupURL})
}

func assertPrimarySucceeded(t *testing.T, res handback.Result, policy *lab.PolicyModel, password string) {
	t.Helper()
	if len(res.Trace.SuccessEvidence) == 0 {
		t.Errorf("expected success evidence (check_logged_in gate): %+v", res.Trace)
	}
	if policy.Calls() == 0 {
		t.Errorf("expected the agent policy to have driven the flow")
	}
	if res.Trace.EntrypointHow != "agent" {
		t.Errorf("expected agent entrypoint, got %q", res.Trace.EntrypointHow)
	}
	if password != "" {
		// The agent-primary trace must never contain the real password.
		for _, line := range res.Trace.AgentTakeover {
			if strings.Contains(line, password) {
				t.Fatalf("password leaked into the agent trace: %q", line)
			}
		}
	}
}

// TestE2EAgentPrimaryPasswordApps runs the agent-primary driver (lab policy)
// across the lab auth apps that need only a password (no verification channel),
// asserting each signs in via the check_logged_in gate.
func TestE2EAgentPrimaryPasswordApps(t *testing.T) {
	skipUnlessE2E(t)
	apps := []string{
		"simple", "multistep", "modal", "iframe", "spa-jwt",
		"hidden-entry", "bound-session", "form-errors", "hydration-login",
	}
	for _, app := range apps {
		app := app
		t.Run(app, func(t *testing.T) {
			h := newHarness(t)
			e := h.expect(app)
			policy := lab.NewPolicyModel()
			res, err := h.runAgentPrimary(t, app, identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, policy, nil)
			if err != nil {
				t.Fatalf("agent-primary %s should succeed: %v (trace %+v)", app, err, res.Trace)
			}
			assertPrimarySucceeded(t, res, policy, e.Password)
		})
	}
}

// TestE2EAgentPrimaryVerifierApps runs the agent-primary driver across the lab
// apps that require a verification step (email code, SMS, magic link, TOTP),
// wiring a lab-inbox + TOTP verifier so the enter_verification tool can fetch the
// code/link. The lab policy infers the channel from the page.
func TestE2EAgentPrimaryVerifierApps(t *testing.T) {
	skipUnlessE2E(t)
	cases := []struct {
		app      string
		addr     string // inbox address the code/link is delivered to
		password string // "" for passwordless
		totpSeed string
	}{
		{"otp-sms", lab.PhoneFor, lab.DefaultPass, ""},
		{"magic-link", lab.DefaultUser, "", ""}, // passwordless email link
		{"totp", "", lab.DefaultPass, lab.TOTPSeed},
		{"combined-verify", lab.DefaultUser, lab.DefaultPass, ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.app, func(t *testing.T) {
			h := newHarness(t)
			e := h.expect(c.app)
			addr := c.addr
			inbox := verify.NewLabInbox(h.ts.URL, func(string) string { return addr })
			inbox.Poll = 150 * time.Millisecond
			var v verify.Provider = inbox
			if c.totpSeed != "" {
				seed := c.totpSeed
				v = verify.Chain{inbox, verify.NewTOTP(func(string) string { return seed })}
			}
			policy := lab.NewPolicyModel()
			creds := identity.Credentials{UsernameOrEmail: e.Username, Password: c.password, Phone: lab.PhoneFor, TOTPSeed: c.totpSeed}
			res, err := h.runAgentPrimary(t, c.app, creds, policy, v)
			if err != nil {
				t.Fatalf("agent-primary %s should succeed: %v (trace %+v)", c.app, err, res.Trace)
			}
			assertPrimarySucceeded(t, res, policy, c.password)
			if len(res.Trace.Verification) == 0 {
				t.Errorf("%s: expected a verification step in the trace: %+v", c.app, res.Trace)
			}
		})
	}
}

func TestE2EAgentPrimarySignupVerify(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	inbox := verify.NewLabInbox(h.ts.URL, func(string) string { return lab.DefaultUser })
	inbox.Poll = 150 * time.Millisecond
	policy := lab.NewPolicyModel()
	creds := identity.Credentials{UsernameOrEmail: lab.DefaultUser, Password: lab.DefaultPass}
	res, err := h.runAgentPrimaryMode(t, "signup-verify", auth.Signup, creds, policy, inbox)
	if err != nil {
		t.Fatalf("agent-primary signup-verify should succeed: %v (trace %+v)", err, res.Trace)
	}
	assertPrimarySucceeded(t, res, policy, lab.DefaultPass)
}

func TestE2EAgentPrimarySimple(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("simple")
	policy := lab.NewPolicyModel()
	res, err := h.runAgentPrimary(t, "simple", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, policy, nil)
	if err != nil {
		t.Fatalf("agent-primary simple login should succeed: %v (trace %+v)", err, res.Trace)
	}
	assertPrimarySucceeded(t, res, policy, e.Password)
}

func TestE2EAgentPrimaryTrustDevice(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-trust")
	policy := lab.NewPolicyModel()
	res, err := h.runAgentPrimary(t, "takeover-trust", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, policy, nil)
	if err != nil {
		t.Fatalf("agent-primary trust-device should succeed: %v (trace %+v)", err, res.Trace)
	}
	assertPrimarySucceeded(t, res, policy, e.Password)
}

func TestE2EAgentPrimaryTermsModal(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-terms")
	policy := lab.NewPolicyModel()
	res, err := h.runAgentPrimary(t, "takeover-terms", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, policy, nil)
	if err != nil {
		t.Fatalf("agent-primary terms-modal should succeed: %v (trace %+v)", err, res.Trace)
	}
	assertPrimarySucceeded(t, res, policy, e.Password)
}

func TestE2EAgentPrimaryInjectionBlocked(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-injection")
	policy := lab.NewPolicyModel()
	res, err := h.runAgentPrimary(t, "takeover-injection", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, policy, nil)
	if err != nil {
		t.Fatalf("agent-primary injection should still complete the legit login: %v (trace %+v)", err, res.Trace)
	}
	assertPrimarySucceeded(t, res, policy, e.Password)
	if h.lab.TakeoverDeleted() {
		t.Fatal("deny-list FAILED: the injected delete-account action was executed")
	}
}

func TestE2EAgentPrimaryResendOTP(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-resend")
	inbox := verify.NewLabInbox(h.ts.URL, func(string) string { return e.Username })
	inbox.Poll = 150 * time.Millisecond
	policy := lab.NewPolicyModel()
	res, err := h.runAgentPrimary(t, "takeover-resend", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, policy, inbox)
	if err != nil {
		t.Fatalf("agent-primary resend-OTP should succeed: %v (trace %+v)", err, res.Trace)
	}
	assertPrimarySucceeded(t, res, policy, e.Password)
	if len(res.Trace.Verification) == 0 {
		t.Errorf("expected a verification step in the trace: %+v", res.Trace)
	}
}

func TestE2EAgentPrimaryWrongPassword(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("simple")
	policy := lab.NewPolicyModel()
	res, err := h.runAgentPrimary(t, "simple",
		identity.Credentials{UsernameOrEmail: e.Username, Password: "definitely-wrong-password"}, policy, nil)
	if err == nil {
		t.Fatalf("a wrong password must fail: %+v", res.Trace)
	}
	if !errors.Is(err, auth.ErrCredentialsRejected) {
		t.Errorf("expected credentials_rejected, got %v (reason=%s)", err, auth.FailReason(err))
	}
}

func TestE2EAgentPrimaryBotWallFails(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-botwall")
	policy := lab.NewPolicyModel()
	res, err := h.runAgentPrimary(t, "takeover-botwall", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, policy, nil)
	if err == nil {
		t.Fatalf("a bot-wall with no human handoff must fail, not complete: %+v", res.Trace)
	}
	// The agent drove (so it saw the wall) but did not authenticate; with no human
	// handoff configured the bot-wall surfaces as a failure rather than a login.
	if policy.Calls() == 0 {
		t.Errorf("expected the agent to have driven up to the bot-wall")
	}
	if errors.Is(err, auth.ErrCredentialsRejected) {
		t.Errorf("a bot-wall is not a credentials rejection: %v", err)
	}
}
