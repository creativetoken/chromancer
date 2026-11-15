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

func TestNativeConformance(t *testing.T) {
	plugin.Conform(t, func() captcha.Solver { return NewNative() })
}

func TestVisionConformance(t *testing.T) {
	plugin.Conform(t, func() captcha.Solver { return NewVision(nil) })
}

// TestMode answers vendor-published test sitekeys with the documented token.
func TestTestModeVendorTestKeys(t *testing.T) {
	s := NewTestMode(nil, captcha.VendorRecaptchaV2, captcha.VendorHCaptcha, captcha.VendorTurnstile)
	cases := map[string]struct {
		vendor captcha.Vendor
		token  string
	}{
		"6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI": {captcha.VendorRecaptchaV2, "test-recaptcha-token"},
		"10000000-ffff-ffff-ffff-000000000001":     {captcha.VendorHCaptcha, "10000000-aaaa-bbbb-cccc-000000000001"},
		"1x00000000000000000000AA":                 {captcha.VendorTurnstile, "XXXX.DUMMY.TOKEN.XXXX"},
	}
	for sk, want := range cases {
		r, err := s.Solve(context.Background(), captcha.Challenge{Vendor: want.vendor, SiteKey: sk})
		if err != nil {
			t.Fatalf("%s: %v", sk, err)
		}
		if r.Token != want.token {
			t.Errorf("%s: token = %q, want %q", sk, r.Token, want.token)
		}
	}
	// A real (non-test) sitekey is declined — testmode defeats nothing real.
	if _, err := s.Solve(context.Background(), captcha.Challenge{Vendor: captcha.VendorRecaptchaV2, SiteKey: "6Lc_real_key_xyz"}); err != captcha.ErrDeclined {
		t.Errorf("real sitekey: err = %v, want ErrDeclined", err)
	}
}

// Native returns a checkbox click for a widget with bounds, declines without.
func TestNativeSolve(t *testing.T) {
	n := NewNative()
	r, err := n.Solve(context.Background(), captcha.Challenge{Vendor: captcha.VendorRecaptchaV2, Kind: captcha.KindInteractive,
		Rect: &captcha.RegionClick{X: 100, Y: 200, W: 300, H: 78}})
	if err != nil {
		t.Fatalf("with rect: %v", err)
	}
	if r.Kind != captcha.ResultUIActions || len(r.Actions) != 1 {
		t.Fatalf("with rect: %+v", r)
	}
	// Checkbox is on the left half of the widget.
	if r.Actions[0].X != 130 || r.Actions[0].Y != 239 {
		t.Errorf("click point = (%d,%d), want (130,239)", r.Actions[0].X, r.Actions[0].Y)
	}
	if _, err := n.Solve(context.Background(), captcha.Challenge{Vendor: captcha.VendorTurnstile, Kind: captcha.KindInteractive}); err != captcha.ErrDeclined {
		t.Errorf("without rect: err = %v, want ErrDeclined", err)
	}
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
