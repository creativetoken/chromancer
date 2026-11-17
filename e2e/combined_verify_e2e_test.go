package e2e

import (
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/verify"
)

// TestE2ECombinedVerify drives a sign-in that keeps email + password visible and
// reveals a one-time-code field in the same form (as Bambu Lab does). The flow
// must detect the code step despite a password input still being present, fetch
// the emailed code from the lab inbox, submit it, and authenticate — rather than
// re-submitting the login form until it is "stuck".
func TestE2ECombinedVerify(t *testing.T) {
	h := newHarness(t)
	e := h.expect("combined-verify")
	inbox := verify.NewLabInbox(h.ts.URL, func(string) string { return e.Username })
	inbox.Poll = 150 * time.Millisecond
	creds := identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}
	res := h.runAuthV(t, "combined-verify", auth.Login, creds, inbox, auth.Hints{LoginURL: e.LoginURL})
	if len(res.Trace.Verification) == 0 {
		t.Fatalf("expected a verification step in trace: %+v", res.Trace)
	}
	if len(res.Trace.SuccessEvidence) == 0 {
		t.Fatalf("expected authentication success evidence: %+v", res.Trace)
	}
}

// TestE2ECombinedVerifyIntl is the same flow with non-English labels and no
// verification wording, so the code field is recognisable only from structural
// signals (numeric inputmode + short maxlength below the credentials). Proves
// the code-step detection is language-independent end-to-end.
func TestE2ECombinedVerifyIntl(t *testing.T) {
	h := newHarness(t)
	e := h.expect("combined-verify-intl")
	inbox := verify.NewLabInbox(h.ts.URL, func(string) string { return e.Username })
	inbox.Poll = 150 * time.Millisecond
	creds := identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}
	res := h.runAuthV(t, "combined-verify-intl", auth.Login, creds, inbox, auth.Hints{LoginURL: e.LoginURL})
	if len(res.Trace.Verification) == 0 {
		t.Fatalf("expected a verification step in trace: %+v", res.Trace)
	}
	if len(res.Trace.SuccessEvidence) == 0 {
		t.Fatalf("expected authentication success evidence: %+v", res.Trace)
	}
}
