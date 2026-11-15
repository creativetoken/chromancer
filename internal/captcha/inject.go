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

// injectJS sets every matching response field to the token, dispatches input
// events, and invokes any registered success callback: a named global
// (data-callback), the client-config callback reCAPTCHA/hCaptcha/Turnstile
// register, or the common lab hooks. `cb` is the data-callback name (may be "").
const injectJS = `(sel, token, cb) => {
  let any = false;
  const setAll = (q) => document.querySelectorAll(q).forEach(el => {
    el.value = token;
    el.dispatchEvent(new Event('input', {bubbles:true}));
    el.dispatchEvent(new Event('change', {bubbles:true}));
    any = true;
  });
  for (const s of sel.split(',')) { setAll(s.trim()); }
  if (!any) { setAll('[data-captcha-token]'); setAll('input[name="captcha-token"]'); }
  // Named global callback (data-callback="fn" or "obj.fn").
  const callNamed = (name) => {
    if (!name) return;
    try {
      const fn = name.split('.').reduce((o,k)=>o&&o[k], window);
      if (typeof fn === 'function') fn(token);
    } catch(e){}
  };
  callNamed(cb);
  if (typeof window.__labCaptchaCallback === 'function') { try { window.__labCaptchaCallback(token); } catch(e){} }
  if (typeof window.onCaptchaSuccess === 'function') { try { window.onCaptchaSuccess(token); } catch(e){} }
  return any;
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
	case ResultText:
		// Type the answer into the text-captcha input (no vendor callback).
		sel := c.fieldSel
		if sel == "" {
			sel = c.Params[ParamInputSel]
		}
		if sel == "" {
			return fmt.Errorf("captcha: text result has no input selector")
		}
		v, err := p.Eval(ctx, injectJS, sel, r.Text, "")
		if err != nil {
			return fmt.Errorf("captcha: type text answer: %w", err)
		}
		if !v.Bool() {
			return fmt.Errorf("captcha: text-captcha input %q not found", sel)
		}
		return nil
	default: // ResultToken
		sel := c.fieldSel
		if sel == "" {
			sel = responseField(c.Vendor)
		}
		// hCaptcha pages often also carry a g-recaptcha-response field.
		if c.Vendor == VendorHCaptcha {
			sel += `, textarea[name="g-recaptcha-response"]`
		}
		v, err := p.Eval(ctx, injectJS, sel, r.Token, c.Params[ParamCallback])
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
