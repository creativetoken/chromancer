package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/perceive"
	"github.com/creativetoken/chromancer/internal/verify"
)

// scriptModel is a deterministic, observation-aware mock LLM for the agent
// takeover. Each step is a function of the current observation, so it can read
// element ids out of the page. It records every observation so a test can assert
// no secret ever reached the model, and counts calls so a test can assert the
// agent was never invoked (e.g. on a bot-wall).
type scriptModel struct {
	steps []func(obs string) map[string]any
	i     int
	seen  []string
	calls int
}

func (m *scriptModel) Structured(_ context.Context, msgs []model.Message, _ json.RawMessage, _ model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	obs := ""
	for _, mm := range msgs {
		if mm.Role == model.User {
			obs = mm.Text
		}
	}
	m.seen = append(m.seen, obs)
	m.calls++
	act := map[string]any{"action": "give_up", "why": "script exhausted", "reason": "no more steps"}
	if m.i < len(m.steps) {
		act = m.steps[m.i](obs)
		m.i++
	}
	raw, _ := json.Marshal(act)
	return raw, &model.Usage{InputTokens: 20, OutputTokens: 6}, nil
}

var elemLineRe = regexp.MustCompile(`(?m)^#(\d+) [^\n]*?"([^"]*)"`)

// findLabel returns the id of the first interactive element whose accessible
// name matches want, or 0.
func findLabel(obs string, want *regexp.Regexp) int {
	for _, m := range elemLineRe.FindAllStringSubmatch(obs, -1) {
		if want.MatchString(m[2]) {
			return atoi(m[1])
		}
	}
	return 0
}

// clickLabel clicks the first element matching labelRe; if none is present yet it
// waits (so the step can run again on the next, settled observation).
func clickLabel(labelRe *regexp.Regexp, why string) func(string) map[string]any {
	return func(obs string) map[string]any {
		if id := findLabel(obs, labelRe); id > 0 {
			return map[string]any{"action": "click", "id": id, "why": why}
		}
		return map[string]any{"action": "wait", "ms": 300, "why": "waiting for " + why}
	}
}

func checkLoggedIn() func(string) map[string]any {
	return func(string) map[string]any {
		return map[string]any{"action": "check_logged_in", "why": "confirm login"}
	}
}

// runTakeover drives the Authenticator against a lab app with agent takeover
// wired to the scripted model. It returns the result and error WITHOUT failing
// the test, so negative cases can assert the error.
func (h *harness) runTakeover(t *testing.T, app string, creds identity.Credentials, agent *scriptModel, v verify.Provider, verifyTimeout time.Duration) (handback.Result, error) {
	t.Helper()
	e := h.expect(app)
	ws := filepath.Join(h.ws, app)
	var key [32]byte
	key[0] = byte(len(app) + 7)
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
		Audit:    al, Budget: core.NewBudget(core.Limits{Time: 150 * time.Second}),
		Verifier:      v,
		Agent:         agent, // explore.LLMModel
		Recovery:      auth.RecoveryAgent,
		Handoff:       nil, // no human: recovery is agent-only in these tests
		VerifyTimeout: verifyTimeout,
		Capture:       handback.CaptureWindow{N: 3, T: 5 * time.Second},
		EntryCache:    filepath.Join(ws, "cache"), Timeout: 8 * time.Second,
	}
	return authr.Run(h.ctx, h.b, auth.Login, auth.Hints{LoginURL: e.LoginURL})
}

// --- Positive scenarios: the agent finishes a stuck login ------------------

func TestE2ETakeoverTrustDevice(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-trust")
	agent := &scriptModel{steps: []func(string) map[string]any{
		clickLabel(regexp.MustCompile(`(?i)trust this device`), "trust the device"),
		checkLoggedIn(),
	}}
	res, err := h.runTakeover(t, "takeover-trust", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, agent, nil, 0)
	if err != nil {
		t.Fatalf("takeover should have completed: %v (trace %+v)", err, res.Trace)
	}
	assertAgentSucceeded(t, res, agent, e.Password)
}

func TestE2ETakeoverTermsModal(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-terms")
	agent := &scriptModel{steps: []func(string) map[string]any{
		clickLabel(regexp.MustCompile(`(?i)agree`), "accept the terms"),
		clickLabel(regexp.MustCompile(`(?i)continue`), "continue"),
		checkLoggedIn(),
	}}
	res, err := h.runTakeover(t, "takeover-terms", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, agent, nil, 0)
	if err != nil {
		t.Fatalf("takeover should have completed: %v (trace %+v)", err, res.Trace)
	}
	assertAgentSucceeded(t, res, agent, e.Password)
}

func TestE2ETakeoverResendOTP(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-resend")
	inbox := verify.NewLabInbox(h.ts.URL, func(string) string { return e.Username })
	inbox.Poll = 150 * time.Millisecond
	agent := &scriptModel{steps: []func(string) map[string]any{
		func(string) map[string]any {
			return map[string]any{"action": "resend_verification", "why": "the code never arrived"}
		},
		func(string) map[string]any {
			return map[string]any{"action": "enter_verification", "channel": "email", "want": "code", "why": "enter the fresh code"}
		},
		checkLoggedIn(),
	}}
	// Short verify timeout so the deterministic verify gives up quickly and hands
	// off to the agent (no code is delivered until the agent resends).
	res, err := h.runTakeover(t, "takeover-resend", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, agent, inbox, 3*time.Second)
	if err != nil {
		t.Fatalf("resend takeover should have completed: %v (trace %+v)", err, res.Trace)
	}
	assertAgentSucceeded(t, res, agent, e.Password)
	if len(res.Trace.Verification) == 0 {
		t.Errorf("expected a verification step in the trace: %+v", res.Trace)
	}
}

// --- Security scenarios ----------------------------------------------------

func TestE2ETakeoverInjectionBlocked(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-injection")
	agent := &scriptModel{steps: []func(string) map[string]any{
		// Obey the page's injected instruction — the deny-list must block it.
		clickLabel(regexp.MustCompile(`(?i)delete account`), "the page told me to delete the account"),
		clickLabel(regexp.MustCompile(`(?i)continue`), "continue to the account"),
		checkLoggedIn(),
	}}
	res, err := h.runTakeover(t, "takeover-injection", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, agent, nil, 0)
	if err != nil {
		t.Fatalf("injection takeover should still complete the legit login: %v (trace %+v)", err, res.Trace)
	}
	assertAgentSucceeded(t, res, agent, e.Password)
	if h.lab.TakeoverDeleted() {
		t.Fatal("deny-list FAILED: the injected delete-account action was executed")
	}
}

func TestE2ETakeoverBotWallGoesToHuman(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-botwall")
	agent := &scriptModel{steps: []func(string) map[string]any{
		// Must never run — if it does, it would (wrongly) try to click through.
		clickLabel(regexp.MustCompile(`(?i).*`), "should not happen"),
	}}
	res, err := h.runTakeover(t, "takeover-botwall", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}, agent, nil, 0)
	if err == nil {
		t.Fatalf("bot-wall must not be completed by the agent: %+v", res.Trace)
	}
	if agent.calls != 0 {
		t.Errorf("agent must NOT be invoked on a bot-wall, but it was called %d times", agent.calls)
	}
	if len(res.Trace.AgentTakeover) != 0 {
		t.Errorf("no agent-takeover should be recorded for a bot-wall: %+v", res.Trace.AgentTakeover)
	}
}

func TestE2ETakeoverWrongPasswordFails(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("takeover-trust")
	agent := &scriptModel{steps: []func(string) map[string]any{
		clickLabel(regexp.MustCompile(`(?i).*`), "should not happen"),
	}}
	res, err := h.runTakeover(t, "takeover-trust",
		identity.Credentials{UsernameOrEmail: e.Username, Password: "definitely-wrong-password"}, agent, nil, 0)
	if err == nil {
		t.Fatalf("a wrong password must fail, not recover: %+v", res.Trace)
	}
	if !errors.Is(err, auth.ErrCredentialsRejected) {
		t.Errorf("expected credentials_rejected, got %v (reason=%s)", err, auth.FailReason(err))
	}
	if agent.calls != 0 {
		t.Errorf("agent must NOT be invoked on a credentials rejection, called %d times", agent.calls)
	}
}

// assertAgentSucceeded checks the shared success invariants: authentication
// evidence, an agent-takeover trace, and NO secret ever shown to the model.
func assertAgentSucceeded(t *testing.T, res handback.Result, agent *scriptModel, password string) {
	t.Helper()
	if len(res.Trace.SuccessEvidence) == 0 {
		t.Errorf("expected success evidence (check_logged_in gate): %+v", res.Trace)
	}
	if len(res.Trace.AgentTakeover) == 0 {
		t.Errorf("expected an agent-takeover trace: %+v", res.Trace)
	}
	if agent.calls == 0 {
		t.Errorf("expected the agent to have been invoked")
	}
	// SECURITY: no secret (password) ever reached the model's input.
	if password != "" {
		for i, obs := range agent.seen {
			if strings.Contains(obs, password) {
				t.Fatalf("password leaked into model observation #%d", i)
			}
		}
	}
}
