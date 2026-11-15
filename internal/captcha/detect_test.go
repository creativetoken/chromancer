package captcha

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/creativetoken/chromancer/internal/perceive"
)

// fixture loads a saved HTML fixture and classifies it through the pure-Go probe
// (the same classifier the browser path uses).
func fixture(t *testing.T, name string) (Challenge, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captcha", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return classify(probeHTML(data), "https://app.example.test")
}

func TestDetectFixtures(t *testing.T) {
	cases := []struct {
		file       string
		wantVendor Vendor
		wantKind   Kind
		sitekey    string
		class      string // expected Params[ParamClass], "" to skip
		action     string // expected Params[ParamAction], "" to skip
		enterprise bool
	}{
		{file: "recaptcha_v2_checkbox.html", wantVendor: VendorRecaptchaV2, wantKind: KindInteractive, sitekey: "6Lc_v2_checkbox_sitekey_AAAA"},
		{file: "recaptcha_v2_invisible.html", wantVendor: VendorRecaptchaV2, wantKind: KindScore, sitekey: "6Lc_v2_invisible_sitekey_BBBB"},
		{file: "recaptcha_v3.html", wantVendor: VendorRecaptchaV3, wantKind: KindScore, sitekey: "6Lc_v3_sitekey_CCCC", action: "login"},
		{file: "recaptcha_enterprise.html", wantVendor: VendorRecaptchaV3, wantKind: KindScore, sitekey: "6Lc_ent_sitekey_DDDD", action: "signin", enterprise: true},
		{file: "hcaptcha.html", wantVendor: VendorHCaptcha, wantKind: KindInteractive, sitekey: "10000000-ffff-ffff-ffff-000000000001"},
		{file: "turnstile_managed.html", wantVendor: VendorTurnstile, wantKind: KindInteractive, sitekey: "0x4AAAAAAA_turnstile_managed", action: "login"},
		{file: "turnstile_invisible.html", wantVendor: VendorTurnstile, wantKind: KindScore, sitekey: "0x4AAAAAAA_turnstile_invis"},
		{file: "cloudflare_interstitial.html", wantVendor: VendorCustom, wantKind: KindCustom, class: "cloudflare_interstitial"},
		{file: "image_grid.html", wantVendor: VendorCustom, wantKind: KindImage, class: "image_grid"},
		{file: "text_captcha.html", wantVendor: VendorCustom, wantKind: KindImage, class: "text"},
		{file: "arkose_funcaptcha.html", wantVendor: VendorArkose, wantKind: KindCustom, class: "arkose"},
		{file: "geetest.html", wantVendor: VendorCustom, wantKind: KindCustom, class: "geetest"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			ch, ok := fixture(t, tc.file)
			if !ok {
				t.Fatalf("%s: expected a challenge, got none", tc.file)
			}
			if ch.Vendor != tc.wantVendor {
				t.Errorf("%s: vendor = %q, want %q", tc.file, ch.Vendor, tc.wantVendor)
			}
			if ch.Kind != tc.wantKind {
				t.Errorf("%s: kind = %q, want %q", tc.file, ch.Kind, tc.wantKind)
			}
			if tc.sitekey != "" && ch.SiteKey != tc.sitekey {
				t.Errorf("%s: sitekey = %q, want %q", tc.file, ch.SiteKey, tc.sitekey)
			}
			if tc.class != "" && ch.Params[ParamClass] != tc.class {
				t.Errorf("%s: class = %q, want %q", tc.file, ch.Params[ParamClass], tc.class)
			}
			if tc.action != "" && ch.Params[ParamAction] != tc.action {
				t.Errorf("%s: action = %q, want %q", tc.file, ch.Params[ParamAction], tc.action)
			}
			if tc.enterprise && ch.Params[ParamEnterprise] != "true" {
				t.Errorf("%s: expected enterprise=true", tc.file)
			}
		})
	}
}

// TestDetectInteractiveExtraction checks the rich params on interactive widgets.
func TestDetectInteractiveExtraction(t *testing.T) {
	ch, ok := fixture(t, "recaptcha_v2_checkbox.html")
	if !ok {
		t.Fatal("no challenge")
	}
	if ch.Params[ParamCallback] != "onRecaptcha" {
		t.Errorf("callback = %q, want onRecaptcha", ch.Params[ParamCallback])
	}
	if ch.fieldSel == "" {
		t.Error("expected a response field selector")
	}
	hc, _ := fixture(t, "hcaptcha.html")
	if hc.Params[ParamRQData] != "rq_abc" {
		t.Errorf("hcaptcha rqdata = %q, want rq_abc", hc.Params[ParamRQData])
	}
	ts, _ := fixture(t, "turnstile_managed.html")
	if ts.Params[ParamCData] != "sess123" {
		t.Errorf("turnstile cdata = %q, want sess123", ts.Params[ParamCData])
	}
}

// TestDetectTextCaptchaInput checks the answer-input selector is extracted.
func TestDetectTextCaptchaInput(t *testing.T) {
	ch, ok := fixture(t, "text_captcha.html")
	if !ok {
		t.Fatal("no challenge")
	}
	if ch.Params[ParamInputSel] != "#captcha_code" {
		t.Errorf("input_sel = %q, want #captcha_code", ch.Params[ParamInputSel])
	}
	if ch.fieldSel != "#captcha_code" {
		t.Errorf("fieldSel = %q, want #captcha_code", ch.fieldSel)
	}
}

// TestNoFalsePositives: plain login pages must not classify as a challenge.
func TestNoFalsePositives(t *testing.T) {
	for _, f := range []string{"plain_login.html", "search_with_image.html"} {
		if ch, ok := fixture(t, f); ok {
			t.Errorf("%s: false positive: %+v", f, ch)
		}
	}
}

// TestBadgeOnlyIsScoreNotInteractive: a page with only a reCAPTCHA v3 badge
// (render= script, no visible widget) is a non-blocking score challenge.
func TestBadgeOnlyIsScoreNotInteractive(t *testing.T) {
	ch, ok := fixture(t, "plain_login_with_badge.html")
	if !ok {
		t.Fatal("expected a score challenge")
	}
	if ch.Kind != KindScore {
		t.Fatalf("badge-only kind = %q, want score (non-blocking)", ch.Kind)
	}
}

// TestDetectFromFailedSubmitBody: no widget on the page, but the rejected login
// POST returned a captcha — detection must fire from the response body.
func TestDetectFromFailedSubmitBody(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captcha", "fixtures", "hcaptcha.html"))
	if err != nil {
		t.Fatal(err)
	}
	d := unifiedDetector{}
	// Empty DOM probe (no page), but a failed-submit body carrying the captcha.
	ch, ok, err := d.Detect(nil, nil, perceive.State{}, NetSignals{FailedSubmit: true, FailedSubmitBody: string(data)})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || ch.Vendor != VendorHCaptcha {
		t.Fatalf("expected hcaptcha from failed-submit body, got ok=%v vendor=%q", ok, ch.Vendor)
	}
}
