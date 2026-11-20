package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/browser"
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

// launchReadFingerprint opens the camoufox backend for an account dir (which
// makes the sidecar generate+persist, or reuse, that account's fingerprint),
// opens a page so the sidecar fully starts, then returns the saved fingerprint
// JSON. Returns "" when the camoufox backend is unavailable (sidecar/Xvfb absent).
func launchReadFingerprint(t *testing.T, accountDir string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	b, err := browser.Open(ctx, "camoufox", browser.Options{Headless: true, AccountDir: accountDir, Logger: t.Logf})
	if err != nil {
		return "" // backend unavailable → caller skips
	}
	defer b.Close()
	p, err := b.NewPage(ctx)
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	_ = p.Close()
	data, err := os.ReadFile(filepath.Join(accountDir, "camoufox.json"))
	if err != nil {
		t.Fatalf("read fingerprint: %v", err)
	}
	var fp struct {
		Fingerprint json.RawMessage `json:"fingerprint"`
	}
	if err := json.Unmarshal(data, &fp); err != nil {
		t.Fatalf("decode fingerprint: %v", err)
	}
	return string(fp.Fingerprint)
}

// TestE2ECamoufoxFingerprintStability proves one account keeps a STABLE
// fingerprint across runs while two accounts get DIFFERENT fingerprints (item 3).
// Skipped automatically when the camoufox sidecar isn't installed.
func TestE2ECamoufoxFingerprintStability(t *testing.T) {
	skipUnlessE2E(t)
	dirA, dirB := t.TempDir(), t.TempDir()

	a1 := launchReadFingerprint(t, dirA)
	if a1 == "" {
		t.Skip("camoufox backend unavailable (sidecar/Xvfb not installed)")
	}
	a2 := launchReadFingerprint(t, dirA) // second run, same account
	bFP := launchReadFingerprint(t, dirB)

	if a1 != a2 {
		t.Errorf("same account must keep a stable fingerprint across runs (differed)")
	}
	if a1 == bFP {
		t.Errorf("two accounts must get different fingerprints (matched)")
	}
	if len(a1) < 100 {
		t.Errorf("fingerprint looks empty/too small (%d bytes)", len(a1))
	}
}
