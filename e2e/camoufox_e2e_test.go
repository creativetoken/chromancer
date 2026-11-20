package e2e

import (
	"testing"

	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// backends returns the backends to exercise in the dual-backend e2e tests.
// chromium always; camoufox only when its sidecar is installed (the harness
// skips a camoufox subtest otherwise).
var backends = []string{"chromium", "camoufox"}

// TestE2ECamoufoxLogin drives a basic cookie login against the lab on the
// Camoufox backend. It is skipped automatically when the Camoufox sidecar/Xvfb
// runtime is not installed (Open fails → newHarnessBackend skips). This proves
// the backend-neutral auth flow runs unchanged on Firefox/Playwright.
func TestE2ECamoufoxLogin(t *testing.T) {
	h := newHarnessBackend(t, "camoufox")
	e := h.expect("simple")
	creds := identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}
	res := h.runAuthV(t, "simple", auth.Login, creds, nil, auth.Hints{LoginURL: e.LoginURL})
	if len(res.Trace.SuccessEvidence) == 0 {
		t.Fatalf("expected a successful camoufox login: %+v", res.Trace)
	}
	if res.Trace.Backend != "camoufox" {
		t.Errorf("trace should record the camoufox backend, got %q", res.Trace.Backend)
	}
}

// TestE2EPerceiveBothBackends runs the shared JS perceiver against the lab login
// page on every available backend and asserts it finds the username/password
// inputs and the submit control. This guards the perceiver's cross-engine DOM
// assumptions (B5). camoufox is skipped when its sidecar isn't installed.
func TestE2EPerceiveBothBackends(t *testing.T) {
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			h := newHarnessBackend(t, backend)
			e := h.expect("simple")
			p, err := h.b.NewPage(h.ctx)
			if err != nil {
				t.Fatalf("new page: %v", err)
			}
			defer p.Close()
			if err := p.Navigate(h.ctx, e.LoginURL); err != nil {
				t.Fatalf("navigate: %v", err)
			}
			st, _, err := perceive.New().Perceive(h.ctx, p)
			if err != nil {
				t.Fatalf("perceive: %v", err)
			}
			var inputs, submits int
			for _, n := range st.Nodes {
				switch {
				case n.Tag == "input":
					inputs++
				case n.Tag == "button" || n.Type == "submit":
					submits++
				}
			}
			if inputs < 2 || submits < 1 {
				t.Errorf("[%s] perceiver found inputs=%d submits=%d on the login form; want >=2 inputs, >=1 submit\nnodes: %s",
					backend, inputs, submits, perceive.Compact(st))
			}
		})
	}
}
