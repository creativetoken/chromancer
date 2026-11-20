// Package e2e runs browser end-to-end tests against the in-process lab. They
// need Chrome and are gated behind AGENTBROWSER_E2E=1 (see `make e2e`).
package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	_ "github.com/creativetoken/chromancer/internal/browser/rodbackend" // register "chromium" backend
	"github.com/creativetoken/chromancer/internal/lab"
)

// harness bundles a running lab and a launched browser for one test.
type harness struct {
	t    *testing.T
	lab  *lab.Server
	ts   *httptest.Server
	b    browser.Browser
	ctx  context.Context
	stop context.CancelFunc
	ws   string
}

func skipUnlessE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTBROWSER_E2E") != "1" {
		t.Skip("set AGENTBROWSER_E2E=1 to run browser e2e tests")
	}
}

// newHarness builds a harness on the backend named by AGENTBROWSER_E2E_BACKEND
// (default "chromium"), so the whole lab suite can be run against either engine.
func newHarness(t *testing.T) *harness {
	backend := os.Getenv("AGENTBROWSER_E2E_BACKEND")
	if backend == "" {
		backend = "chromium"
	}
	return newHarnessBackend(t, backend)
}

// newHarnessBackend builds a harness on a named browser backend. For a backend
// whose sidecar/runtime isn't installed (e.g. camoufox without Xvfb), Open fails
// and the test is skipped rather than failed.
func newHarnessBackend(t *testing.T, backend string) *harness {
	t.Helper()
	skipUnlessE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	s := lab.New()
	ts := httptest.NewServer(s.Handler())
	s.SetBase(ts.URL)
	ws := t.TempDir()
	b, err := browser.Open(ctx, backend, browser.Options{Headless: true, ProfileDir: ws + "/profile", AccountDir: ws, Logger: t.Logf})
	if err != nil {
		cancel()
		ts.Close()
		if backend != "chromium" {
			t.Skipf("%s backend unavailable (sidecar/runtime not installed): %v", backend, err)
		}
		t.Fatalf("launch browser: %v", err)
	}
	h := &harness{t: t, lab: s, ts: ts, b: b, ctx: ctx, stop: cancel, ws: ws}
	t.Cleanup(h.close)
	return h
}

func (h *harness) close() {
	if h.b != nil {
		_ = h.b.Close()
		h.b = nil
	}
	if h.ts != nil {
		h.ts.Close()
	}
	h.stop()
}

func (h *harness) expect(app string) lab.Expect {
	e, ok := h.lab.ExpectFor(app)
	if !ok {
		h.t.Fatalf("unknown lab app %s", app)
	}
	return e
}
