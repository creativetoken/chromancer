package captcha

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// domProbe is the result of a single JS scan of the page for widget markers.
type domProbe struct {
	Scripts []string    `json:"scripts"`
	Iframes []string    `json:"iframes"`
	Widgets []widgetHit `json:"widgets"`
}

type widgetHit struct {
	Vendor   string `json:"vendor"`
	SiteKey  string `json:"sitekey"`
	FieldSel string `json:"field_sel"`
	Visible  bool   `json:"visible"`
}

// probeJS scans the DOM for known CAPTCHA widget signatures and returns JSON.
const probeJS = `() => {
  const scripts = Array.from(document.scripts).map(s => s.src).filter(Boolean);
  const iframes = Array.from(document.querySelectorAll('iframe')).map(f => f.src).filter(Boolean);
  const widgets = [];
  const add = (vendor, el, fieldSel) => {
    const sk = el.getAttribute('data-sitekey') || el.getAttribute('data-site-key') || '';
    const r = el.getBoundingClientRect();
    widgets.push({ vendor, sitekey: sk, field_sel: fieldSel, visible: r.width > 0 && r.height > 0 });
  };
  document.querySelectorAll('.g-recaptcha, div[data-sitekey].recaptcha, #g-recaptcha').forEach(el => add('recaptcha_v2', el, 'textarea[name="g-recaptcha-response"], #g-recaptcha-response'));
  document.querySelectorAll('.h-captcha').forEach(el => add('hcaptcha', el, 'textarea[name="h-captcha-response"], textarea[name="g-recaptcha-response"]'));
  document.querySelectorAll('.cf-turnstile').forEach(el => add('turnstile', el, 'input[name="cf-turnstile-response"]'));
  document.querySelectorAll('.lab-captcha, [data-lab-captcha]').forEach(el => add('mock', el, 'input[name="captcha-token"], input[data-captcha-token]'));
  return JSON.stringify({ scripts, iframes, widgets });
}`

func probe(ctx context.Context, p *browser.Page) (domProbe, error) {
	var out domProbe
	if p == nil {
		return out, nil
	}
	v, err := p.Eval(ctx, probeJS)
	if err != nil {
		return out, err
	}
	_ = json.Unmarshal([]byte(v.Str()), &out)
	return out, nil
}

func anyContains(ss []string, sub string) (string, bool) {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return s, true
		}
	}
	return "", false
}

// signatureDetector detects one vendor from DOM widgets + script srcs.
type signatureDetector struct {
	vendor    Vendor
	widgetTag string   // widget vendor string emitted by probeJS
	scriptSig []string // any of these in a script/iframe src
	kind      Kind
}

func (d signatureDetector) Vendor() Vendor { return d.vendor }

func (d signatureDetector) Detect(ctx context.Context, p *browser.Page, st perceive.State, sig NetSignals) (Challenge, bool, error) {
	pr, err := probe(ctx, p)
	if err != nil {
		// Fall back to script signals only.
		pr = domProbe{Scripts: sig.ScriptSrcs}
	}
	all := append(append([]string{}, pr.Scripts...), pr.Iframes...)
	all = append(all, sig.ScriptSrcs...)

	for _, wgt := range pr.Widgets {
		if wgt.Vendor == d.widgetTag {
			return Challenge{Vendor: d.vendor, Kind: d.kind, SiteKey: wgt.SiteKey, PageURL: origin(p), fieldSel: wgt.FieldSel}, true, nil
		}
	}
	for _, s := range d.scriptSig {
		if src, ok := anyContains(all, s); ok {
			// Invisible/score vendors (recaptcha v3) have a script but no widget.
			sk := siteKeyFromRender(src)
			return Challenge{Vendor: d.vendor, Kind: d.kind, SiteKey: sk, PageURL: origin(p)}, true, nil
		}
	}
	return Challenge{}, false, nil
}

func siteKeyFromRender(src string) string {
	if i := strings.Index(src, "render="); i >= 0 {
		rest := src[i+len("render="):]
		if j := strings.IndexAny(rest, "&#"); j >= 0 {
			rest = rest[:j]
		}
		if rest != "explicit" && rest != "onload" {
			return rest
		}
	}
	return ""
}

func origin(p *browser.Page) string {
	if p == nil {
		return ""
	}
	return p.Origin()
}

// DefaultDetectors returns the built-in detectors in priority order (visible
// interactive widgets first, invisible/score last).
func DefaultDetectors() []Detector {
	return []Detector{
		MockDetector{},
		signatureDetector{vendor: VendorRecaptchaV2, widgetTag: "recaptcha_v2", scriptSig: []string{"recaptcha/api.js", "recaptcha/api2/anchor"}, kind: KindInteractive},
		signatureDetector{vendor: VendorHCaptcha, widgetTag: "hcaptcha", scriptSig: []string{"hcaptcha.com/1/api.js", "hcaptcha.com/captcha"}, kind: KindInteractive},
		signatureDetector{vendor: VendorTurnstile, widgetTag: "turnstile", scriptSig: []string{"challenges.cloudflare.com/turnstile"}, kind: KindInteractive},
		signatureDetector{vendor: VendorRecaptchaV3, widgetTag: "", scriptSig: []string{"recaptcha/api.js?render="}, kind: KindScore},
	}
}

// MockDetector detects the lab's mock widget.
type MockDetector struct{}

// Vendor implements Detector.
func (MockDetector) Vendor() Vendor { return VendorMock }

// Detect implements Detector.
func (MockDetector) Detect(ctx context.Context, p *browser.Page, st perceive.State, sig NetSignals) (Challenge, bool, error) {
	pr, err := probe(ctx, p)
	if err != nil {
		return Challenge{}, false, nil
	}
	for _, w := range pr.Widgets {
		if w.Vendor == "mock" {
			fs := w.FieldSel
			if fs == "" {
				fs = `input[name="captcha-token"]`
			}
			return Challenge{Vendor: VendorMock, Kind: KindInteractive, SiteKey: w.SiteKey, PageURL: origin(p), fieldSel: fs}, true, nil
		}
	}
	return Challenge{}, false, nil
}

// DetectAny runs detectors in order and returns the first challenge found.
func DetectAny(ctx context.Context, dets []Detector, p *browser.Page, st perceive.State, sig NetSignals) (Challenge, bool) {
	for _, d := range dets {
		if c, ok, err := d.Detect(ctx, p, st, sig); err == nil && ok {
			return c, true
		}
	}
	return Challenge{}, false
}
