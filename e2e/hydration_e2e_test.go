package e2e

import (
	"testing"

	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/identity"
)

// TestE2EHydrationLogin drives a login against a slow-hydrating SPA whose submit
// control is only wired up after a delayed re-render that detaches the node the
// authenticator first perceived. The deterministic flow must not strand on the
// skeleton submit: it should wait for an actionable control and/or re-perceive a
// fresh handle and retry, completing the login. (Guards the fix for the Uniqlo
// UK "submit: browser: click: context deadline exceeded" hydration race.)
func TestE2EHydrationLogin(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("hydration-login")
	creds := identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}
	res := h.runAuthV(t, "hydration-login", auth.Login, creds, nil, auth.Hints{LoginURL: e.LoginURL})
	if len(res.Trace.SuccessEvidence) == 0 {
		t.Fatalf("expected authentication success against the hydrating form: %+v", res.Trace)
	}
}
