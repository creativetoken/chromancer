package e2e

import (
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/verify"
)

// TestE2EPasswordlessSignup: a signup form with only an email field (no
// password). The signup handler fills the email and submits; the emailed code
// is fetched and verified; the account is created and signed in.
func TestE2EPasswordlessSignup(t *testing.T) {
	h := newHarness(t)
	e := h.expect("pwless-signup")
	inbox := verify.NewLabInbox(h.ts.URL, nil)
	inbox.Poll = 150 * time.Millisecond
	inbox.Addr = func(string) string { return e.Username }
	creds := identity.Credentials{UsernameOrEmail: e.Username} // no password
	res := h.runAuthV(t, "pwless-signup", auth.Signup, creds, inbox, auth.Hints{SignupURL: e.SignupURL})
	if len(res.Trace.Verification) == 0 {
		t.Fatalf("expected a verification step: %+v", res.Trace)
	}
}
