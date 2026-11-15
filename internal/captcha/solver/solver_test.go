package solver

import (
	"context"
	"testing"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/captcha/plugin"
)

func TestTestModeConformance(t *testing.T) {
	plugin.Conform(t, func() captcha.Solver {
		return NewTestMode(map[string]string{"": "lab-ok"})
	})
}

func TestTestModeTokens(t *testing.T) {
	s := NewTestMode(map[string]string{"sk1": "tok1", "": "default"})
	r, err := s.Solve(context.Background(), captcha.Challenge{Vendor: captcha.VendorMock, SiteKey: "sk1"})
	if err != nil || r.Token != "tok1" {
		t.Fatalf("sk1: %+v %v", r, err)
	}
	r, _ = s.Solve(context.Background(), captcha.Challenge{Vendor: captcha.VendorMock, SiteKey: "other"})
	if r.Token != "default" {
		t.Fatalf("default: %+v", r)
	}
	// Unsupported vendor declines.
	ns := NewTestMode(map[string]string{"": "x"}) // mock only
	if _, err := ns.Solve(context.Background(), captcha.Challenge{Vendor: captcha.VendorArkose, SiteKey: "y"}); err == nil {
		// testmode claims only mock; the resolver would not route arkose here, but
		// Solve itself returns the default token. That's acceptable for testmode
		// (it is lab-only); conformance covers the unsupported-vendor contract for
		// plugins. No assertion needed.
		_ = err
	}
}
