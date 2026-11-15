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
//
// Visibility is judged by computed style, not bounding-box size: an *invisible*
// reCAPTCHA still renders a visibly-sized anchor/badge iframe and a full-width
// (but visibility:hidden) bframe, so size alone false-positives. An interactive
// challenge is only real when its widget container is shown, or its challenge
// popup (bframe / hCaptcha challenge frame) is actually made visible.
const probeJS = `() => {
  const scripts = Array.from(document.scripts).map(s => s.src).filter(Boolean);
  const iframes = Array.from(document.querySelectorAll('iframe')).map(f => f.src).filter(Boolean);
  const widgets = [];
  const shown = (el) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && cs.display !== 'none' && cs.visibility !== 'hidden' && cs.opacity !== '0';
  };
  const add = (vendor, el, fieldSel) => {
    const sk = el.getAttribute('data-sitekey') || el.getAttribute('data-site-key') || '';
    widgets.push({ vendor, sitekey: sk, field_sel: fieldSel, visible: shown(el) });
  };
  document.querySelectorAll('.g-recaptcha, div[data-sitekey].recaptcha, #g-recaptcha').forEach(el => add('recaptcha_v2', el, 'textarea[name="g-recaptcha-response"], #g-recaptcha-response'));
  document.querySelectorAll('.h-captcha').forEach(el => add('hcaptcha', el, 'textarea[name="h-captcha-response"], textarea[name="g-recaptcha-response"]'));
  document.querySelectorAll('.cf-turnstile').forEach(el => add('turnstile', el, 'input[name="cf-turnstile-response"]'));
  document.querySelectorAll('.lab-captcha, [data-lab-captcha]').forEach(el => add('mock', el, 'input[name="captcha-token"], input[data-captcha-token]'));
  // An actively shown challenge popup is a real interactive block even without a
  // widget container. The popup iframe is injected hidden at rest and only made
  // visible when a challenge fires, so require genuine (computed) visibility.
  const addPopup = (vendor, f, fieldSel) => { if (shown(f)) widgets.push({ vendor, sitekey: '', field_sel: fieldSel, visible: true }); };
  document.querySelectorAll('iframe[src*="api2/bframe"]').forEach(f => addPopup('recaptcha_v2', f, 'textarea[name="g-recaptcha-response"], #g-recaptcha-response'));
  document.querySelectorAll('iframe[src*="hcaptcha.com"]').forEach(f => { if (/frame=challenge/.test(f.src)) addPopup('hcaptcha', f, 'textarea[name="h-captcha-response"]'); });
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
			// An interactive challenge must actually be visible. An invisible
			// widget (reCAPTCHA "invisible"/Enterprise, data-size="invisible")
			// executes on submit and attaches its own token — there is nothing
			// for a solver or a human to do, so it is not a blocking challenge.
			if d.kind == KindInteractive && !wgt.Visible {
				continue
			}
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
		// Interactive vendors fire ONLY on a visibly-shown widget or challenge
		// popup (judged by computed style in probeJS). Script/iframe presence is
		// deliberately NOT a signal: invisible/Enterprise reCAPTCHA, invisible
		// hCaptcha and managed Turnstile all load their script and inject hidden
		// iframes with no challenge for a human to solve — matching those
		// false-positives into a dead-end human handoff.
		signatureDetector{vendor: VendorRecaptchaV2, widgetTag: "recaptcha_v2", kind: KindInteractive},
		signatureDetector{vendor: VendorHCaptcha, widgetTag: "hcaptcha", kind: KindInteractive},
		signatureDetector{vendor: VendorTurnstile, widgetTag: "turnstile", kind: KindInteractive},
		// Score vendors (reCAPTCHA v3, with an explicit render key) are detected
		// from the script but are non-blocking: the flow submits and the page
		// attaches the token. Never a human handoff.
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
