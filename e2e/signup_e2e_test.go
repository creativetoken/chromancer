package e2e

import (
	"context"
	"path/filepath"
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

// runAuthV drives the Authenticator against a lab app with a verifier.
func (h *harness) runAuthV(t *testing.T, app string, mode auth.Mode, creds identity.Credentials, v verify.Provider, hints auth.Hints) handback.Result {
	t.Helper()
	e := h.expect(app)
	ws := filepath.Join(h.ws, app)
	var key [32]byte
	key[0] = byte(len(app) + int(mode))
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
		Audit:    al, Budget: core.NewBudget(core.Limits{Time: 120 * time.Second}),
		Verifier: v, Capture: handback.CaptureWindow{N: 3, T: 5 * time.Second},
		EntryCache: filepath.Join(ws, "cache"), Timeout: 8 * time.Second,
	}
	res, err := authr.Run(h.ctx, h.b, mode, hints)
	if err != nil {
		t.Fatalf("%s %s: %v (trace: %+v)", app, mode, err, res.Trace)
	}
	return res
}

func TestE2ESignupVerify(t *testing.T) {
	h := newHarness(t)
	e := h.expect("signup-verify")
	inbox := verify.NewLabInbox(h.ts.URL, nil) // account == email
	inbox.Poll = 150 * time.Millisecond
	creds := identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}
	// account id "signup-verify" but inbox addressed by email: use Addr mapping.
	inbox.Addr = func(string) string { return e.Username }
	res := h.runAuthV(t, "signup-verify", auth.Signup, creds, inbox, auth.Hints{SignupURL: e.SignupURL})
	if len(res.Trace.Verification) == 0 {
		t.Fatalf("expected a verification step in trace: %+v", res.Trace)
	}
}

func TestE2EMagicLink(t *testing.T) {
	h := newHarness(t)
	e := h.expect("magic-link")
	inbox := verify.NewLabInbox(h.ts.URL, func(string) string { return e.Username })
	inbox.Poll = 150 * time.Millisecond
	creds := identity.Credentials{UsernameOrEmail: e.Username}
	res := h.runAuthV(t, "magic-link", auth.Login, creds, inbox, auth.Hints{LoginURL: e.LoginURL})
	if len(res.Trace.Verification) == 0 {
		t.Fatalf("expected magic-link verification: %+v", res.Trace)
	}
}

func TestE2EOTPSMS(t *testing.T) {
	h := newHarness(t)
	e := h.expect("otp-sms")
	inbox := verify.NewLabInbox(h.ts.URL, func(string) string { return lab.PhoneFor })
	inbox.Poll = 150 * time.Millisecond
	creds := identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password, Phone: lab.PhoneFor}
	h.runAuthV(t, "otp-sms", auth.Login, creds, inbox, auth.Hints{LoginURL: e.LoginURL})
}

func TestE2ETOTP(t *testing.T) {
	h := newHarness(t)
	e := h.expect("totp")
	totp := verify.NewTOTP(func(string) string { return lab.TOTPSeed })
	creds := identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password, TOTPSeed: lab.TOTPSeed}
	h.runAuthV(t, "totp", auth.Login, creds, totp, auth.Hints{LoginURL: e.LoginURL})
}

func TestE2EFormErrors(t *testing.T) {
	h := newHarness(t)
	e := h.expect("form-errors")
	creds := identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}
	res := h.runAuthV(t, "form-errors", auth.Login, creds, nil, auth.Hints{LoginURL: e.LoginURL})
	// The app rejects the first submit; the trace should record a recovery.
	found := false
	for _, s := range res.Trace.Steps {
		if containsStr(s, "form error") || containsStr(s, "recover") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected form-error recovery in trace: %+v", res.Trace.Steps)
	}
}

func TestE2ESSOOnlyFails(t *testing.T) {
	h := newHarness(t)
	e := h.expect("sso-only")
	ws := filepath.Join(h.ws, "sso")
	var key [32]byte
	al := audit.NewNop()
	router := &model.StaticRouter{Default: model.NewMock(func(string) string { return `{"handle":0}` })}
	authr := &auth.Authenticator{
		Target: e.Root, Account: "sso-only", Creds: identity.Credentials{UsernameOrEmail: e.Username, Password: "x"},
		Perceiver: perceive.New(), Router: router,
		Sessions: &auth.FileSessionStore{Path: filepath.Join(ws, "s.enc"), Key: &key},
		Writer:   &handback.FileWriter{Dir: filepath.Join(ws, "art"), Account: "sso-only"},
		Audit:    al, Budget: core.NewBudget(core.Limits{Time: 60 * time.Second}),
		EntryCache: filepath.Join(ws, "cache"), Timeout: 8 * time.Second,
	}
	_, err := authr.Run(h.ctx, h.b, auth.Login, auth.Hints{LoginURL: e.LoginURL})
	if auth.FailReason(err) != "sso_only" {
		t.Fatalf("expected sso_only, got %v (reason %q)", err, auth.FailReason(err))
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}

var _ = context.Background
