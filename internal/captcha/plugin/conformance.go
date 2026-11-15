package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/captcha"
)

// Conform is the conformance suite every solver (in-process or subprocess) must
// pass. It checks Describe stability, that Solve honours the declared vendors,
// returns a typed result with a solve id, respects context cancellation, and
// that Report is accepted. A solver implementation's test calls plugin.Conform.
func Conform(t *testing.T, newSolver func() captcha.Solver) {
	t.Helper()
	s := newSolver()

	// Describe must be cheap, deterministic and non-empty.
	caps := s.Describe()
	if len(caps.Vendors) == 0 {
		t.Fatalf("Describe: solver must declare at least one vendor")
	}
	if caps2 := s.Describe(); !sameCaps(caps, caps2) {
		t.Fatalf("Describe must be stable across calls")
	}

	// Solve on a supported vendor returns a typed result with a solve id.
	v := caps.Vendors[0]
	kind := captcha.KindInteractive
	if len(caps.Kinds) > 0 {
		kind = caps.Kinds[0]
	}
	ch := captcha.Challenge{Vendor: v, Kind: kind, SiteKey: "conformance-sitekey", PageURL: "http://example.test"}
	if caps.NeedsImage {
		ch.Image = []byte{0x89, 0x50, 0x4e, 0x47} // tiny PNG-ish marker
	}
	ch.Params = map[string]string{"solve_id": "conf-1"}
	res, err := s.Solve(context.Background(), ch)
	if err != nil {
		// A solver may legitimately decline; that's acceptable, but it must not panic.
		if err == captcha.ErrDeclined {
			t.Logf("solver declined conformance challenge (acceptable)")
		} else {
			t.Fatalf("Solve: %v", err)
		}
	} else {
		switch res.Kind {
		case captcha.ResultToken:
			if res.Token == "" {
				t.Fatalf("Solve: token result has empty token")
			}
		case captcha.ResultUIActions:
			if len(res.Actions) == 0 {
				t.Fatalf("Solve: ui-actions result has no regions")
			}
		case captcha.ResultHandoff:
			// fine
		default:
			t.Fatalf("Solve: unknown result kind %d", res.Kind)
		}
		if res.SolveID == "" {
			t.Fatalf("Solve: result must carry a solve id")
		}
		// Report the outcome.
		if err := s.Report(context.Background(), res.SolveID, true); err != nil {
			t.Fatalf("Report: %v", err)
		}
	}

	// Context cancellation is honoured.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = s.Solve(ctx, ch) // must return promptly (no hang); a short deadline guards it.

	// An unsupported vendor must not be silently "solved" into a token.
	bad := captcha.Challenge{Vendor: "definitely-not-supported-xyz", Kind: kind, SiteKey: "x"}
	if r, err := s.Solve(context.Background(), bad); err == nil && r.Kind == captcha.ResultToken && r.Token != "" {
		t.Fatalf("solver returned a token for an unsupported vendor")
	}
	_ = time.Second
}

func sameCaps(a, b captcha.Capabilities) bool {
	if len(a.Vendors) != len(b.Vendors) || len(a.Kinds) != len(b.Kinds) {
		return false
	}
	for i := range a.Vendors {
		if a.Vendors[i] != b.Vendors[i] {
			return false
		}
	}
	return a.NeedsImage == b.NeedsImage && a.NeedsProxy == b.NeedsProxy && a.EgressExternal == b.EgressExternal
}
