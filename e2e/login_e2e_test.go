package e2e

import (
	"context"
	"os"
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
)

// runLogin drives the full Authenticator against one lab app and returns the
// result plus the components (so tests can assert on the saved session/audit).
func (h *harness) runLogin(t *testing.T, app string, hint bool) (handback.Result, *auth.FileSessionStore, string) {
	t.Helper()
	e := h.expect(app)
	ws := filepath.Join(h.ws, app)
	var key [32]byte
	key[0] = byte(len(app))
	red := audit.NewRedactor()
	red.AddSecret(lab.DefaultPass)
	auditPath := filepath.Join(ws, "audit.jsonl")
	al, err := audit.OpenFile(auditPath, red)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	sessions := &auth.FileSessionStore{Path: filepath.Join(ws, "session.enc"), Key: &key}
	router := &model.StaticRouter{Default: model.NewMock(func(string) string { return `{"handle":0,"reason":"mock"}` })}

	hints := auth.Hints{}
	if hint {
		hints.LoginURL = e.LoginURL
	}
	authr := &auth.Authenticator{
		Target: e.Root, Account: app, Creds: identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password},
		Perceiver: perceive.New(), Router: router, Sessions: sessions,
		Writer: &handback.FileWriter{Dir: filepath.Join(ws, "artefacts"), Account: app},
		Audit:  al, Budget: core.NewBudget(core.Limits{Time: 90 * time.Second}),
		Capture: handback.CaptureWindow{N: 3, T: 5 * time.Second}, EntryCache: filepath.Join(ws, "cache"),
		Timeout: 8 * time.Second,
	}
	res, err := authr.Run(h.ctx, h.b, auth.Login, hints)
	if err != nil {
		t.Fatalf("%s login failed: %v", app, err)
	}
	return res, sessions, auditPath
}

func TestE2ELoginAllM1Apps(t *testing.T) {
	cases := []struct {
		app  string
		hint bool
	}{
		{"simple", false},      // discovered via link
		{"multistep", false},   // identifier-first
		{"modal", false},       // modal opened from header button
		{"iframe", false},      // form in an iframe
		{"spa-jwt", false},     // bearer in localStorage
		{"hidden-entry", true}, // non-obvious entry; hint provided (LLM fallback is mock-declined)
		{"bound-session", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.app, func(t *testing.T) {
			h := newHarness(t)
			e := h.expect(c.app)
			res, _, auditPath := h.runLogin(t, c.app, c.hint)

			// Read back the written bundle.
			w := &handback.FileWriter{Dir: filepath.Dir(res.BundleRef.Path), Account: c.app}
			bundle, err := w.ReadBundle(h.ctx, res.BundleRef)
			if err != nil {
				t.Fatal(err)
			}
			// Bearer apps must carry the Authorization header; cookie apps must carry cookies.
			switch e.AuthMechanism {
			case "bearer":
				if bundle.AuthHeaders["authorization"] == "" {
					t.Fatalf("%s: bundle missing bearer auth header: %+v", c.app, bundle.AuthHeaders)
				}
			case "cookie":
				if len(bundle.Cookies) == 0 {
					t.Fatalf("%s: bundle has no cookies", c.app)
				}
			}
			// Replayability expectation.
			if bundle.Replayable != e.Replayable {
				t.Fatalf("%s: replayable=%v want %v (reason %q)", c.app, bundle.Replayable, e.Replayable, bundle.ReplayReason)
			}
			if !e.Replayable && bundle.ReplayReason == "" {
				t.Fatalf("%s: expected a non-replayable reason", c.app)
			}
			// Audit log must not contain the plaintext password.
			raw, _ := os.ReadFile(auditPath)
			if strings.Contains(string(raw), lab.DefaultPass) {
				t.Fatalf("%s: plaintext password leaked into audit log", c.app)
			}
			if broken, _ := al0(t, auditPath).Verify(context.Background()); broken != 0 {
				t.Fatalf("%s: audit chain broken at %d", c.app, broken)
			}
		})
	}
}

func al0(t *testing.T, path string) audit.AuditLog {
	t.Helper()
	l, err := audit.OpenFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// TestE2ERestoreWithoutLogin verifies a second run restores the saved session
// and performs no login flow.
func TestE2ERestoreWithoutLogin(t *testing.T) {
	h := newHarness(t)
	// First run authenticates.
	res1, _, _ := h.runLogin(t, "simple", false)
	if res1.Trace.Restored {
		t.Fatal("first run should not be a restore")
	}
	// Second run (same workspace) must restore.
	res2, _, _ := h.runLogin(t, "simple", false)
	if !res2.Trace.Restored {
		t.Fatalf("second run should restore without login; trace: %+v", res2.Trace)
	}
	if res2.Trace.FlowVariant != "" {
		t.Fatalf("restore should not run a flow handler, got variant %q", res2.Trace.FlowVariant)
	}
}
