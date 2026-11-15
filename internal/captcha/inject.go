package captcha

import (
	"context"
	"fmt"

	"github.com/creativetoken/chromancer/internal/browser"
)

// genericInjector sets the vendor response field value and dispatches events,
// and invokes a JS success callback if the page registered one. It works for
// token-style results across the mock widget and the standard vendor response
// textareas; UIActions results are clicked by the core on the page.
type genericInjector struct {
	vendor Vendor
}

// Vendor implements Injector.
func (g genericInjector) Vendor() Vendor { return g.vendor }

const injectJS = `(sel, token) => {
  let el = null;
  for (const s of sel.split(',')) { el = document.querySelector(s.trim()); if (el) break; }
  if (!el) {
    // Some widgets expect a hidden input created on demand.
    el = document.querySelector('[data-captcha-token]') || document.querySelector('input[name="captcha-token"]');
  }
  if (el) {
    el.value = token;
    el.dispatchEvent(new Event('input', {bubbles:true}));
    el.dispatchEvent(new Event('change', {bubbles:true}));
  }
  // Fire a page-registered callback if present (lab + common patterns).
  if (typeof window.__labCaptchaCallback === 'function') { try { window.__labCaptchaCallback(token); } catch(e){} }
  if (typeof window.onCaptchaSuccess === 'function') { try { window.onCaptchaSuccess(token); } catch(e){} }
  return !!el;
}`

// Apply implements Injector.
func (g genericInjector) Apply(ctx context.Context, p *browser.Page, c Challenge, r Result) error {
	switch r.Kind {
	case ResultHandoff:
		return nil // the human already acted
	case ResultUIActions:
		for _, a := range r.Actions {
			if err := p.ClickAt(ctx, a.X+a.W/2, a.Y+a.H/2); err != nil {
				return fmt.Errorf("captcha: click region: %w", err)
			}
		}
		return nil
	default: // ResultToken
		sel := c.fieldSel
		if sel == "" {
			sel = responseField(c.Vendor)
		}
		v, err := p.Eval(ctx, injectJS, sel, r.Token)
		if err != nil {
			return fmt.Errorf("captcha: inject token: %w", err)
		}
		if !v.Bool() {
			return fmt.Errorf("captcha: no response field found for %s", c.Vendor)
		}
		return nil
	}
}

func responseField(v Vendor) string {
	switch v {
	case VendorRecaptchaV2, VendorRecaptchaV3:
		return `textarea[name="g-recaptcha-response"], #g-recaptcha-response`
	case VendorHCaptcha:
		return `textarea[name="h-captcha-response"]`
	case VendorTurnstile:
		return `input[name="cf-turnstile-response"]`
	default:
		return `input[name="captcha-token"], [data-captcha-token]`
	}
}

// DefaultInjector returns a generic injector for a vendor.
func DefaultInjector(v Vendor) Injector { return genericInjector{vendor: v} }
