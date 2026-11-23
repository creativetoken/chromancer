package camoufox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	pw "github.com/mxschmitt/playwright-go"

	"github.com/creativetoken/chromancer/internal/browser"
)

// clickDebugf emits click/type diagnostics to stderr when AGENTBROWSER_CLICK_DEBUG
// is set. It is a no-op otherwise, so it is free on the normal path. The output
// (per-step durations, errors, and the target's live actionability) is what turns
// a reproduction — in the lab or in a single instrumented live run — into a
// definitive diagnosis instead of a guess.
func clickDebugf(format string, a ...interface{}) {
	if os.Getenv("AGENTBROWSER_CLICK_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "[click-debug] "+format+"\n", a...)
}

// errStr renders an error for a debug line with a secret scrubbed out — Playwright
// echoes the argument it was given (e.g. `fill("867730")`) into its error text, so
// the typed OTP/password must be removed before logging.
func errStr(err error, secret string) string {
	if err == nil {
		return "<nil>"
	}
	s := err.Error()
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "<redacted>")
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 { // drop Playwright's multi-line call log
		s = s[:i]
	}
	return s
}

// dumpOTPDOM returns a structural snapshot (NO values) of the element the type
// handle points to and every candidate code input on the page — tag, id, name,
// autocomplete, box, shadow-root membership — so a failed OTP entry shows the REAL
// DOM shape instead of being guessed at. Best-effort; empty string when debug off.
func dumpOTPDOM(ctx context.Context, p *cfPage, el pw.ElementHandle) string {
	if os.Getenv("AGENTBROWSER_CLICK_DEBUG") == "" {
		return ""
	}
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var out string
	_ = p.run(sctx, func() error {
		v, err := el.Evaluate(`el => {
			function info(n){ if(!n) return null; var r=n.getBoundingClientRect();
				return {tag:n.tagName, id:n.id||null, name:n.getAttribute&&n.getAttribute('name'),
					type:n.getAttribute&&n.getAttribute('type'), ac:n.getAttribute&&n.getAttribute('autocomplete'),
					handle:n.getAttribute&&n.getAttribute('data-ab-handle'),
					w:Math.round(r.width), h:Math.round(r.height), x:Math.round(r.left), y:Math.round(r.top),
					inShadow:(n.getRootNode&&n.getRootNode()!==document)}; }
			var handle = info(el);
			var parents=[]; var p=el.parentElement; for(var i=0;i<4&&p;i++){ parents.push(info(p)); p=p.parentElement; }
			var cand = [].slice.call(document.querySelectorAll('input[autocomplete="one-time-code"], input[name="code"], input[inputmode="numeric"], input[type="text"]')).map(info);
			return JSON.stringify({handle:handle, parents:parents, candidates:cand});
		}`)
		if err != nil {
			out = "dump-err: " + err.Error()
			return nil
		}
		if s, ok := v.(string); ok {
			out = s
		}
		return nil
	})
	return out
}

// coordFallbackDisabled reports whether the trusted coordinate click/type fallback
// is turned off. It defaults ON; AGENTBROWSER_DISABLE_COORD_FALLBACK=1 reverts to
// the handle-only behaviour (a kill-switch, and the lever that makes the backend
// trusted-modal e2e deterministically reproduce the pre-fix failure).
func coordFallbackDisabled() bool {
	return os.Getenv("AGENTBROWSER_DISABLE_COORD_FALLBACK") == "1"
}

// probeState returns a one-line snapshot of el's live actionability: whether it
// is still attached, its rect, whether it is the hit-test target at its own
// centre (an overlay intercepting pointer events is the classic actionable-click
// killer), and whether it is disabled. Best-effort; never blocks more than ~1s.
func probeState(ctx context.Context, p *cfPage, el pw.ElementHandle) string {
	if os.Getenv("AGENTBROWSER_CLICK_DEBUG") == "" {
		return ""
	}
	sctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var out string
	_ = p.run(sctx, func() error {
		v, err := el.Evaluate(`el => {
			if (!el.isConnected) return {attached:false};
			const r = el.getBoundingClientRect();
			const cx = r.left + r.width/2, cy = r.top + r.height/2;
			const top = document.elementFromPoint(cx, cy);
			return {
				attached:true, w:Math.round(r.width), h:Math.round(r.height),
				x:Math.round(cx), y:Math.round(cy),
				disabled: !!el.disabled,
				hitSelf: top === el,
				hit: top ? (top.tagName+(top.id?('#'+top.id):'')) : null,
			};
		}`)
		if err != nil {
			out = "probe-err: " + err.Error()
			return nil
		}
		b, _ := json.Marshal(v)
		out = string(b)
		return nil
	})
	return out
}

// cfValue wraps a Playwright Evaluate result as a neutral browser.Value.
type cfValue struct{ v interface{} }

func (c cfValue) Str() string {
	if s, ok := c.v.(string); ok {
		return s
	}
	if c.v == nil {
		return ""
	}
	b, _ := json.Marshal(c.v)
	return string(b)
}
func (c cfValue) Bool() bool {
	b, _ := c.v.(bool)
	return b
}
func (c cfValue) Int() int {
	if f, ok := c.v.(float64); ok {
		return int(f)
	}
	return 0
}
func (c cfValue) Float() float64 {
	f, _ := c.v.(float64)
	return f
}
func (c cfValue) Nil() bool { return c.v == nil }
func (c cfValue) Unmarshal(x any) error {
	b, err := json.Marshal(c.v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, x)
}

// evaluate runs a JS function in fr. Playwright passes a SINGLE argument to the
// function, so when the caller supplies several (as rod allows) they are bundled
// into one array and the expression is wrapped to spread them, keeping the same
// shared JS (e.g. the perceiver's walkJS) working on both backends.
func evaluate(eval func(string, ...interface{}) (interface{}, error), js string, args ...interface{}) (browser.Value, error) {
	var (
		res interface{}
		err error
	)
	switch len(args) {
	case 0:
		res, err = eval(js)
	case 1:
		res, err = eval(js, args[0])
	default:
		wrapped := "(__a) => (" + js + ")(...__a)"
		res, err = eval(wrapped, args)
	}
	if err != nil {
		return nil, fmt.Errorf("camoufox: eval: %w", err)
	}
	return cfValue{v: res}, nil
}

// cfFrame wraps a Playwright frame.
type cfFrame struct{ fr pw.Frame }

func (f *cfFrame) Eval(ctx context.Context, js string, args ...interface{}) (browser.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return evaluate(f.fr.Evaluate, js, args...)
}

// cfElement wraps a Playwright element handle.
type cfElement struct{ el pw.ElementHandle }

func asHandle(e browser.Element) (pw.ElementHandle, error) {
	ce, ok := e.(*cfElement)
	if !ok || ce == nil || ce.el == nil {
		return nil, fmt.Errorf("camoufox: foreign or nil element handle %T", e)
	}
	return ce.el, nil
}

// cfPage is one tab backed by a Playwright page.
type cfPage struct {
	pg   pw.Page
	bctx pw.BrowserContext
	b    *cfBrowser
}

var _ browser.Page = (*cfPage)(nil)

// timeoutMs derives a Playwright timeout (ms) from the ctx deadline. nil = no
// explicit timeout (Playwright's default still applies).
func timeoutMs(ctx context.Context) *float64 {
	dl, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	ms := float64(time.Until(dl) / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	return &ms
}

// run executes a blocking Playwright call while honouring ctx: it returns
// ctx.Err() as soon as ctx is done, without waiting for the call (playwright-go
// has no context parameter). Each call also carries a Playwright timeout derived
// from the ctx deadline, so the backing goroutine unwinds on its own; the page
// is NOT closed here — a per-operation deadline (e.g. an 8s perceive) must not
// tear down the whole session, and true job cancellation is made prompt by the
// browser Close() on teardown.
func (p *cfPage) run(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *cfPage) Navigate(ctx context.Context, url string) error {
	if err := p.run(ctx, func() error {
		_, err := p.pg.Goto(url, pw.PageGotoOptions{WaitUntil: pw.WaitUntilStateLoad, Timeout: timeoutMs(ctx)})
		return err
	}); err != nil {
		return fmt.Errorf("camoufox: navigate %s: %w", url, err)
	}
	return p.WaitStable(ctx, browser.DefaultStableTimeout)
}

func (p *cfPage) Reload(ctx context.Context) error {
	if err := p.run(ctx, func() error {
		_, err := p.pg.Reload(pw.PageReloadOptions{WaitUntil: pw.WaitUntilStateLoad, Timeout: timeoutMs(ctx)})
		return err
	}); err != nil {
		return fmt.Errorf("camoufox: reload: %w", err)
	}
	return p.WaitStable(ctx, browser.DefaultStableTimeout)
}

func (p *cfPage) URL() string { return p.pg.URL() }

func (p *cfPage) Title() string {
	t, err := p.pg.Title()
	if err != nil {
		return ""
	}
	return t
}

// WaitStable waits for the network to go idle, bounded by timeout. Like the
// chromium backend it never errors on a mere timeout — a page that keeps polling
// is still "stable enough" to act on.
func (p *cfPage) WaitStable(ctx context.Context, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ms := float64(timeout / time.Millisecond)
	_ = p.run(ctx, func() error {
		return p.pg.WaitForLoadState(pw.PageWaitForLoadStateOptions{
			State:   pw.LoadStateNetworkidle,
			Timeout: &ms,
		})
	})
	return nil
}

func (p *cfPage) Eval(ctx context.Context, js string, args ...interface{}) (browser.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return evaluate(p.pg.Evaluate, js, args...)
}

func (p *cfPage) Frames(ctx context.Context) ([]browser.Frame, error) {
	frs := p.pg.Frames()
	out := make([]browser.Frame, 0, len(frs))
	// Ensure the main frame is index 0.
	main := p.pg.MainFrame()
	out = append(out, &cfFrame{fr: main})
	for _, fr := range frs {
		if fr == main {
			continue
		}
		out = append(out, &cfFrame{fr: fr})
	}
	return out, nil
}

func (p *cfPage) Screenshot(ctx context.Context) ([]byte, error) {
	var data []byte
	err := p.run(ctx, func() error {
		b, e := p.pg.Screenshot(pw.PageScreenshotOptions{Type: pw.ScreenshotTypePng})
		data = b
		return e
	})
	if err != nil {
		return nil, fmt.Errorf("camoufox: screenshot: %w", err)
	}
	return data, nil
}

func (p *cfPage) Element(ctx context.Context, frame browser.Frame, selector string, timeout time.Duration) (browser.Element, error) {
	ms := float64(timeout / time.Millisecond)
	var el pw.ElementHandle
	err := p.run(ctx, func() error {
		var e error
		if frame != nil {
			cf, ok := frame.(*cfFrame)
			if !ok || cf == nil || cf.fr == nil {
				return fmt.Errorf("camoufox: foreign or nil frame handle %T", frame)
			}
			el, e = cf.fr.WaitForSelector(selector, pw.FrameWaitForSelectorOptions{State: pw.WaitForSelectorStateAttached, Timeout: &ms})
		} else {
			el, e = p.pg.WaitForSelector(selector, pw.PageWaitForSelectorOptions{State: pw.WaitForSelectorStateAttached, Timeout: &ms})
		}
		return e
	})
	if err != nil {
		return nil, fmt.Errorf("camoufox: element %q: %w", selector, err)
	}
	return &cfElement{el: el}, nil
}

// WaitElementReady waits for the element to be visible, enabled and stable
// (box not animating), bounded by the element's context. Mirrors the chromium
// backend's readiness gate so the hydration race is closed on both.
func (p *cfPage) WaitElementReady(ctx context.Context, e browser.Element, stableFor time.Duration) error {
	el, err := asHandle(e)
	if err != nil {
		return err
	}
	ms := float64(stableFor / time.Millisecond)
	for _, st := range []*pw.ElementState{pw.ElementStateVisible, pw.ElementStateEnabled, pw.ElementStateStable} {
		state := *st
		if err := p.run(ctx, func() error {
			return el.WaitForElementState(state, pw.ElementHandleWaitForElementStateOptions{Timeout: &ms})
		}); err != nil {
			return fmt.Errorf("camoufox: wait %v: %w", state, err)
		}
	}
	return nil
}

// Click clicks an element robustly, mirroring the rodbackend contract: a real
// Playwright click first (full mouse/pointer events, waits for the target to be
// actionable — visible, stable, enabled, unobscured), and when that cannot land,
// a JS click dispatched ON THE ELEMENT ITSELF. The element-level fallback ignores
// overlays and actionability gating and always hits the intended element, so for
// a submit/confirm button it still triggers the form submit.
//
// Two things this gets right that an earlier version did not:
//   - Budget: callers bound a click to a short deadline (actor.bound ~ 8s). The
//     actionable attempt is capped at a slice of that budget so the JS fallback is
//     always reachable; an earlier version let the first attempt consume the whole
//     8s, which surfaced as the bambulab Confirm "click: context deadline exceeded".
//   - No Force click: a Playwright Force click skips actionability but still
//     dispatches a real mouse event at a POINT, so over a transparent overlay it
//     "succeeds" by clicking the overlay — a false positive that never submits the
//     form. The element-level JS click cannot mis-target like that.
func (p *cfPage) Click(ctx context.Context, e browser.Element) error {
	el, err := asHandle(e)
	if err != nil {
		return err
	}
	p.scrollIntoView(ctx, el)

	remaining := func() time.Duration {
		if dl, ok := ctx.Deadline(); ok {
			return time.Until(dl)
		}
		return 8 * time.Second
	}
	if remaining() <= 0 {
		return fmt.Errorf("camoufox: click: %w", context.DeadlineExceeded)
	}
	clickDebugf("click: budget=%v state=%s", remaining().Round(time.Millisecond), probeState(ctx, p, el))

	// 1) Playwright's actionable click — the clean path for a normal control. Capped
	//    short (≤3s, always reserving budget) so an animating / never-"stable" target
	//    cannot burn the whole budget before the trusted coordinate fallback runs.
	primary := remaining() - 3*time.Second
	if primary > 3*time.Second {
		primary = 3 * time.Second
	}
	if primary > 0 {
		sctx, cancel := context.WithTimeout(ctx, primary)
		ms := float64(primary / time.Millisecond)
		t0 := time.Now()
		cerr := p.run(sctx, func() error { return el.Click(pw.ElementHandleClickOptions{Timeout: &ms}) })
		cancel()
		clickDebugf("click: actionable took=%v err=%v", time.Since(t0).Round(time.Millisecond), cerr)
		if cerr == nil {
			return nil
		}
	}

	// If the handle has no bounding box it is a zero-size wrapper (the live bambulab
	// verify modal resolves the Confirm/code handles to <div> wrappers). The
	// handle-based fallbacks cannot help — a JS el.click() on the wrapper is a no-op
	// that still reports success, and a coordinate click has no point — so find the
	// REAL visible control by the handle's label/text and click that.
	if !coordFallbackDisabled() && !p.elementHasBox(ctx, el) {
		clickDebugf("click: handle has no box -> real-target dom=%s", dumpClickDOM(ctx, p, el))
		if p.coordClickRealTarget(ctx, el) {
			clickDebugf("click: real-target landed=yes")
			return nil
		}
		clickDebugf("click: real-target landed=no")
	}

	// The actionable click failed — obscured, or (the live bambulab case) the modal
	// animates every frame so the target is never "stable". Fall through to two
	// dispatches that do NOT wait on actionability. Both are best-effort: neither can
	// confirm the page reacted, so the caller re-perceives and retries if the form
	// did not advance. We run both because each covers a different trap.
	var landed bool
	var lastErr error

	// 2) Element-level JS click — dispatches ON the element, ignoring hit-testing, so
	//    it reaches a target under a transparent overlay (the verify-autosend case)
	//    where a coordinate click would hit the overlay. The event is UNTRUSTED, so a
	//    trusted-event-gated handler ignores it; step 3 covers that. Run FIRST so that
	//    the trusted coordinate click is the LAST dispatch — a navigation it triggers
	//    must not be raced (and aborted) by a following el.Evaluate. If this click DOES
	//    submit (overlay on an idle page), step 3 then finds the element detached and
	//    simply no-ops.
	if rem := remaining(); rem > 0 {
		sctx, cancel := context.WithTimeout(ctx, rem)
		t := time.Now()
		jerr := p.run(sctx, func() error { _, e := el.Evaluate(`el => el.click()`); return e })
		cancel()
		clickDebugf("click: js-fallback took=%v err=%v", time.Since(t).Round(time.Millisecond), jerr)
		if jerr == nil {
			landed = true
		} else {
			lastErr = jerr
		}
	}

	// 3) Trusted coordinate click at the element's centre — a real browser mouse event
	//    (move+down+up), no actionability wait, no page-JS, mirroring go-rod's CDP
	//    click on the chromium backend (which logs into bambulab). The one path that
	//    lands on an animating control whose handler requires a TRUSTED event
	//    (isTrusted) — the live camoufox failure. LAST so the navigation it triggers is
	//    not aborted by any following dispatch.
	if rem := remaining(); rem > 0 && !coordFallbackDisabled() {
		t := time.Now()
		cerr := p.coordClick(ctx, el, rem)
		clickDebugf("click: coord took=%v err=%v", time.Since(t).Round(time.Millisecond), cerr)
		if cerr == nil {
			landed = true
		} else if !landed {
			lastErr = cerr
		}
	}

	if landed {
		return nil
	}
	if lastErr == nil {
		lastErr = context.DeadlineExceeded
	}
	return fmt.Errorf("camoufox: click: %w", lastErr)
}

// scrollIntoView best-effort brings el into view, bounded SHORT. ElementHandle's
// scroll-into-view waits for the element to be "stable" and defaults to a 30s
// timeout, so on an animating / never-"stable" control (the live bambulab modal)
// an unbounded call silently drains the whole action budget before the click/type
// even starts — leaving the budget negative and every step skipped. Capped to ~1s
// and run through p.run so the caller's context also applies; failure is ignored
// (the coordinate paths use BoundingBox and tolerate an element not scrolled).
func (p *cfPage) scrollIntoView(ctx context.Context, el pw.ElementHandle) {
	sctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	to := float64(800)
	_ = p.run(sctx, func() error {
		return el.ScrollIntoViewIfNeeded(pw.ElementHandleScrollIntoViewIfNeededOptions{Timeout: &to})
	})
}

// coordClick dispatches a TRUSTED mouse click at el's centre using the page's
// virtual mouse. Unlike ElementHandle.Click it does not wait for the target to be
// "actionable" (visible+stable+enabled+hit-testable), so it lands on a control that
// animates every frame or that a guarded page never settles — the way go-rod's CDP
// click does on the chromium backend. Bounded by budget.
func (p *cfPage) coordClick(ctx context.Context, el pw.ElementHandle, budget time.Duration) error {
	sctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return p.run(sctx, func() error {
		box, err := el.BoundingBox()
		if err != nil {
			return err
		}
		if box == nil || box.Width <= 0 || box.Height <= 0 {
			return fmt.Errorf("no bounding box")
		}
		cx := box.X + box.Width/2
		cy := box.Y + box.Height/2
		m := p.pg.Mouse()
		if err := m.Move(cx, cy); err != nil {
			return err
		}
		return m.Click(cx, cy)
	})
}

// elementHasBox reports whether el currently has a positive on-screen bounding box.
// A zero-size handle is a wrapper (the live bambulab verify modal resolves the
// Confirm/code handles to collapsed <div> wrappers), which the handle-based
// click/type paths cannot act on. Defaults to true on a probe failure so the normal
// path is never skipped for a healthy element.
func (p *cfPage) elementHasBox(ctx context.Context, el pw.ElementHandle) bool {
	sctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	has := true
	_ = p.run(sctx, func() error {
		v, err := el.Evaluate(`el => { var r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0; }`)
		if err != nil {
			return err
		}
		if b, ok := v.(bool); ok {
			has = b
		}
		return nil
	})
	return has
}

// coordClickRealTarget handles a click whose handle is a zero-size wrapper: it reads
// the handle's visible label and finds the real VISIBLE control (a button/link/
// submit whose text matches, or a confirm/verify/submit control when the label
// implies one) and clicks it by trusted coordinate. Returns whether it dispatched.
func (p *cfPage) coordClickRealTarget(ctx context.Context, el pw.ElementHandle) bool {
	sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var cx, cy float64
	var found bool
	_ = p.run(sctx, func() error {
		v, err := el.Evaluate(`el => {
			var label = ((el.textContent||'') + ' ' + (el.getAttribute('aria-label')||'') + ' ' + (el.value||'')).trim().toLowerCase();
			var wantSubmit = /(confirm|verify|submit|continue|sign in|log in)/.test(label);
			var sel = 'button, [role="button"], input[type="submit"], input[type="button"], a[href]';
			for (var n of document.querySelectorAll(sel)) {
				var r = n.getBoundingClientRect();
				if (r.width <= 0 || r.height <= 0) continue;
				var t = ((n.textContent||'') + ' ' + (n.getAttribute('aria-label')||'') + ' ' + (n.value||'')).trim().toLowerCase();
				var textMatch = label && t && (t.indexOf(label) >= 0 || label.indexOf(t) >= 0);
				var submitMatch = wantSubmit && (n.type === 'submit' || /(confirm|verify|submit|continue)/.test(t));
				if (textMatch || submitMatch) {
					return {x: r.left + r.width/2, y: r.top + r.height/2};
				}
			}
			return null;
		}`)
		if err != nil {
			return err
		}
		if m, ok := v.(map[string]interface{}); ok {
			if x, ok := m["x"].(float64); ok {
				cx, found = x, true
			}
			if y, ok := m["y"].(float64); ok {
				cy = y
			}
		}
		return nil
	})
	if !found {
		return false
	}
	_ = p.run(sctx, func() error {
		m := p.pg.Mouse()
		if err := m.Move(cx, cy); err != nil {
			return err
		}
		return m.Click(cx, cy)
	})
	return true
}

// dumpClickDOM returns a structural snapshot (no values) of a zero-size click
// handle and every visible clickable on the page, so a failed wrapper-handle click
// shows the real control layout instead of being guessed at. Empty when debug off.
func dumpClickDOM(ctx context.Context, p *cfPage, el pw.ElementHandle) string {
	if os.Getenv("AGENTBROWSER_CLICK_DEBUG") == "" {
		return ""
	}
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var out string
	_ = p.run(sctx, func() error {
		v, err := el.Evaluate(`el => {
			function info(n){ if(!n) return null; var r=n.getBoundingClientRect();
				return {tag:n.tagName, id:n.id||null, role:n.getAttribute&&n.getAttribute('role'),
					label:((n.textContent||'').trim().slice(0,24))||null,
					w:Math.round(r.width), h:Math.round(r.height), x:Math.round(r.left), y:Math.round(r.top)}; }
			var handle = info(el);
			var cand = [].slice.call(document.querySelectorAll('button, [role="button"], input[type="submit"], a[href]'))
				.map(info).filter(function(c){ return c && c.w>0 && c.h>0; });
			return JSON.stringify({handle:handle, clickables:cand});
		}`)
		if err != nil {
			out = "dump-err: " + err.Error()
			return nil
		}
		if s, ok := v.(string); ok {
			out = s
		}
		return nil
	})
	return out
}

// coordType focuses el with a trusted coordinate mouse click, selects any existing
// content, and types the text with the page keyboard — real, TRUSTED key events
// with no actionability wait, mirroring go-rod's CDP typing. It is the type
// counterpart of coordClick for a controlled field that ignores untrusted input
// events or that never settles for ElementHandle.Type. Bounded by budget.
func (p *cfPage) coordType(ctx context.Context, el pw.ElementHandle, text string, budget time.Duration) error {
	sctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return p.run(sctx, func() error {
		box, err := el.BoundingBox()
		if err != nil {
			return err
		}
		if box == nil || box.Width <= 0 || box.Height <= 0 {
			return fmt.Errorf("no bounding box")
		}
		cx := box.X + box.Width/2
		cy := box.Y + box.Height/2
		m := p.pg.Mouse()
		if err := m.Move(cx, cy); err != nil {
			return err
		}
		if err := m.Click(cx, cy); err != nil {
			return err
		}
		kb := p.pg.Keyboard()
		_ = kb.Press("Control+a") // select existing content so the type replaces it
		delay := float64(20)
		return kb.Type(text, pw.KeyboardTypeOptions{Delay: &delay})
	})
}

func (p *cfPage) ClickAt(ctx context.Context, x, y int) error {
	m := p.pg.Mouse()
	if err := p.run(ctx, func() error { return m.Move(float64(x), float64(y)) }); err != nil {
		return fmt.Errorf("camoufox: move: %w", err)
	}
	if err := p.run(ctx, func() error { return m.Click(float64(x), float64(y)) }); err != nil {
		return fmt.Errorf("camoufox: click at: %w", err)
	}
	return nil
}

// TypeInto focuses the element, clears it, and types with real key events and a
// small per-key delay (human-like pacing). Each step is best-effort: a guarded
// one-time-code field (readonly until clicked, or under a transient overlay)
// rejects a programmatic clear/type, so — mirroring the rodbackend path — the
// value is verified and, if it did not land, the field is un-guarded with a real
// click and the value set directly with input/change events a framework listens
// for. A failed clear must NOT abort the whole verification step (this was the
// camoufox bambulab OTP failure: el.Fill("") errored and nothing recovered it).
func (p *cfPage) TypeInto(ctx context.Context, e browser.Element, text string) error {
	el, err := asHandle(e)
	if err != nil {
		return err
	}
	p.scrollIntoView(ctx, el)
	remaining := func() time.Duration {
		if dl, ok := ctx.Deadline(); ok {
			return time.Until(dl)
		}
		return 8 * time.Second
	}
	// One slow step at most before the fallbacks. A guarded/animating field makes the
	// real-key attempt block until timeout, so it is capped SHORT and the budget that
	// remains is reserved for the trusted coordinate fallback and the submit click
	// that follows — overrunning here is what left the submit click with an expired
	// context ("camoufox: click: context deadline exceeded", empty field).
	short := func(d time.Duration, fn func() error) error {
		sctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return p.run(sctx, fn)
	}
	stepMs := float64(1500)
	delay := float64(20)
	force := true
	readValue := func() string {
		var got string
		_ = short(time.Second, func() error {
			v, verr := el.Evaluate("el => el.value")
			if s, ok := v.(string); ok {
				got = s
			}
			return verr
		})
		return got
	}
	// landed reports WHETHER the typed value is in the field, never the value itself —
	// the field may hold a password/OTP, which must never be logged.
	landed := func() string {
		if readValue() == text {
			return "yes"
		}
		return "no"
	}
	clickDebugf("type: budget=%v len=%d", remaining().Round(time.Millisecond), len(text))
	// Normal path (best-effort — errors swallowed; the fallbacks below recover). Use
	// Playwright's own focus/clear/type: a raw `el.value=''` desyncs a framework-
	// controlled input (React/Vue) so the subsequent keys do not register, which broke
	// the modal / spa-jwt logins. Fill/Type do not wait for pointer "stability", so
	// they still work on an animating field.
	_ = short(2*time.Second, func() error { return el.Focus() })
	_ = short(2*time.Second, func() error { return el.Fill("", pw.ElementHandleFillOptions{Force: &force, Timeout: &stepMs}) })
	t0 := time.Now()
	_ = short(2*time.Second, func() error {
		return el.Type(text, pw.ElementHandleTypeOptions{Delay: &delay, Timeout: &stepMs})
	})
	clickDebugf("type: real-keys took=%v landed=%s budgetLeft=%v", time.Since(t0).Round(time.Millisecond), landed(), remaining().Round(time.Millisecond))
	// Canonicalise with Playwright's Fill EVEN WHEN the keystrokes appear to have
	// landed. On a React-controlled input, real keystrokes can leave el.value correct
	// while React's internal state is stale/partial — so a form submit sends a
	// MISMATCHED value: live, the bambulab Confirm was enabled and clicked yet the
	// fresh, correct-looking code was rejected. Fill sets the value through the browser
	// protocol and fires a React-visible input event, forcing React's state to match.
	// (It also recovers the camoufox anti-detect prototype-patching case that breaks
	// the native setter in fallback B, and is a no-op risk when the field is already
	// correct.)
	ferr := short(3*time.Second, func() error { return el.Fill(text, pw.ElementHandleFillOptions{Force: &force, Timeout: &stepMs}) })
	clickDebugf("type: fill err=%s landed=%s", errStr(ferr, text), landed())
	if readValue() == text {
		return nil
	}
	// Fallback B: NATIVE value setter + input/change, so a React/Vue-controlled input
	// registers the change (a plain `el.value = v` is silently reverted). Wrapped in
	// try/catch so camoufox's patched prototype cannot throw and mask the later steps.
	var jerr error
	_ = short(2*time.Second, func() error {
		_, jerr = el.Evaluate(`(el, v) => { try {
			el.focus();
			var proto = (el.tagName === 'TEXTAREA') ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
			var d = Object.getOwnPropertyDescriptor(proto, 'value');
			if (d && d.set) { d.set.call(el, ''); d.set.call(el, v); } else { el.value = ''; el.value = v; }
			el.dispatchEvent(new Event("input", { bubbles: true }));
			el.dispatchEvent(new Event("change", { bubbles: true }));
		} catch (e) {} }`, text)
		return jerr
	})
	clickDebugf("type: native err=%s landed=%s", errStr(jerr, text), landed())
	if readValue() == text {
		return nil
	}
	// Fallback C: TRUSTED coordinate entry — focus by a real mouse click and type with
	// the page keyboard (real key events, no actionability wait), mirroring go-rod's
	// CDP typing. The last resort for a field that none of the above can set.
	budget := remaining() - 2*time.Second // reserve budget for the submit click that follows
	if budget > 3*time.Second {
		budget = 3 * time.Second
	}
	clickDebugf("type: -> coord budget=%v", budget.Round(time.Millisecond))
	if budget > 0 && !coordFallbackDisabled() {
		cerr := p.coordType(ctx, el, text, budget)
		clickDebugf("type: coord err=%s landed=%s", errStr(cerr, text), landed())
		if readValue() == text {
			return nil
		}
	}
	// Fallback D: the handle is not the real input — Fill reported "not an <input>",
	// or it has no bounding box: a zero-size wrapper <div> while the actual OTP input
	// is elsewhere (the live bambulab case, confirmed by the debug: hit="DIV" w=0 h=0).
	// Find the real VISIBLE one-time-code input and type into IT by coordinate.
	if !coordFallbackDisabled() {
		if ok := p.coordTypeRealInput(ctx, text, delay); ok {
			clickDebugf("type: real-input landed=yes")
			return nil
		}
		clickDebugf("type: real-input landed=no dom=%s", dumpOTPDOM(ctx, p, el))
	}
	// Nothing landed the value; report the most specific failure.
	switch {
	case ferr != nil:
		return fmt.Errorf("camoufox: type: %s", errStr(ferr, text))
	case jerr != nil:
		return fmt.Errorf("camoufox: type: %s", errStr(jerr, text))
	default:
		return fmt.Errorf("camoufox: type: value did not land in the field")
	}
}

// coordTypeRealInput handles the case where the type handle points to a non-input
// wrapper (a zero-size <div> from a React remount) while the real OTP <input> is
// elsewhere: it finds the first VISIBLE one-time-code / code / numeric input, types
// into it by trusted coordinate keyboard, and verifies that input's own value.
func (p *cfPage) coordTypeRealInput(ctx context.Context, text string, delay float64) bool {
	sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	const sel = `input[autocomplete="one-time-code"], input[name="code"], input[inputmode="numeric"]`
	var cx, cy float64
	var found bool
	_ = p.run(sctx, func() error {
		v, err := p.pg.Evaluate(`(sel) => {
			for (const el of document.querySelectorAll(sel)) {
				const r = el.getBoundingClientRect();
				if (r.width > 0 && r.height > 0) return {x: r.left + r.width/2, y: r.top + r.height/2};
			}
			return null;
		}`, sel)
		if err != nil {
			return err
		}
		if m, ok := v.(map[string]interface{}); ok {
			if x, ok := m["x"].(float64); ok {
				cx, found = x, true
			}
			if y, ok := m["y"].(float64); ok {
				cy = y
			}
		}
		return nil
	})
	if !found {
		return false
	}
	_ = p.run(sctx, func() error {
		m := p.pg.Mouse()
		if err := m.Move(cx, cy); err != nil {
			return err
		}
		if err := m.Click(cx, cy); err != nil {
			return err
		}
		kb := p.pg.Keyboard()
		_ = kb.Press("Control+a")
		return kb.Type(text, pw.KeyboardTypeOptions{Delay: &delay})
	})
	var got string
	_ = p.run(sctx, func() error {
		v, err := p.pg.Evaluate(`(sel) => { for (const el of document.querySelectorAll(sel)) { const r = el.getBoundingClientRect(); if (r.width > 0 && r.height > 0) return el.value || ''; } return ''; }`, sel)
		if s, ok := v.(string); ok {
			got = s
		}
		return err
	})
	return got == text
}

// TypeText types into the focused element without a handle (visual/coordinate
// mode). Real key events are dispatched so framework-controlled inputs update.
func (p *cfPage) TypeText(ctx context.Context, text string) error {
	kb := p.pg.Keyboard()
	delay := float64(20)
	if err := p.run(ctx, func() error { return kb.Type(text, pw.KeyboardTypeOptions{Delay: &delay}) }); err != nil {
		return fmt.Errorf("camoufox: type text: %w", err)
	}
	return nil
}

func (p *cfPage) SelectOption(ctx context.Context, e browser.Element, value string) error {
	el, err := asHandle(e)
	if err != nil {
		return err
	}
	vals := pw.SelectOptionValues{Values: &[]string{value}}
	if err := p.run(ctx, func() error { _, e := el.SelectOption(vals); return e }); err != nil {
		// Fall back to matching by visible label.
		labels := pw.SelectOptionValues{Labels: &[]string{value}}
		if err2 := p.run(ctx, func() error { _, e := el.SelectOption(labels); return e }); err2 != nil {
			return fmt.Errorf("camoufox: select %q: %w", value, err)
		}
	}
	return nil
}

func (p *cfPage) PressEnter(ctx context.Context, e browser.Element) error {
	el, err := asHandle(e)
	if err != nil {
		return err
	}
	if err := p.run(ctx, func() error { return el.Press("Enter") }); err != nil {
		return fmt.Errorf("camoufox: press enter: %w", err)
	}
	return nil
}

// ExpectPopup runs trigger and returns the popup Page it opens (login/magic-link
// in a new window), bounded by timeout. The popup inherits the account context.
func (p *cfPage) ExpectPopup(ctx context.Context, trigger func() error, timeout time.Duration) (browser.Page, error) {
	tmo := float64(timeout / time.Millisecond)
	var np pw.Page
	err := p.run(ctx, func() error {
		pg, e := p.pg.ExpectPopup(func() error { return trigger() }, pw.PageExpectPopupOptions{Timeout: &tmo})
		np = pg
		return e
	})
	if err != nil {
		return nil, fmt.Errorf("camoufox: expect popup: %w", err)
	}
	if np == nil {
		return nil, fmt.Errorf("camoufox: expect popup: no page opened")
	}
	return &cfPage{pg: np, bctx: p.bctx, b: p.b}, nil
}

func (p *cfPage) Back(ctx context.Context) error {
	if err := p.run(ctx, func() error { _, e := p.pg.GoBack(pw.PageGoBackOptions{Timeout: timeoutMs(ctx)}); return e }); err != nil {
		return fmt.Errorf("camoufox: back: %w", err)
	}
	return p.WaitStable(ctx, browser.DefaultStableTimeout)
}

func (p *cfPage) Close() error { return p.pg.Close() }

func (p *cfPage) Origin() string {
	u := p.URL()
	i := strings.Index(u, "://")
	if i < 0 {
		return u
	}
	rest := u[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	return u[:i+3] + rest
}

// pwResourceType maps Playwright's lowercase resource types to the CDP-style
// capitalised Type the AuthRecorder and replayability check expect, so the
// recorder is unchanged across backends.
var pwResourceType = map[string]string{
	"document": "Document", "stylesheet": "Stylesheet", "image": "Image",
	"media": "Media", "font": "Font", "script": "Script", "texttrack": "TextTrack",
	"xhr": "XHR", "fetch": "Fetch", "eventsource": "EventSource",
	"websocket": "WebSocket", "manifest": "Manifest", "ping": "Ping", "other": "Other",
}

func cdpType(rt string) string {
	if t, ok := pwResourceType[strings.ToLower(rt)]; ok {
		return t
	}
	return rt
}

func lowerHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[strings.ToLower(k)] = v
	}
	return out
}

// Subscribe streams network events to sink via Playwright's typed request/
// response listeners. Bodies are read as each response finishes; redirect
// bodies are skipped and oversized bodies are capped, and a "body unavailable"
// never aborts the recording.
func (p *cfPage) Subscribe(ctx context.Context, sink browser.NetworkSink) (cancel func()) {
	var stopped atomic.Bool

	p.pg.OnRequest(func(r pw.Request) {
		if stopped.Load() {
			return
		}
		ev := browser.RequestEvent{
			ID: requestID(r), At: time.Now().UTC(), Method: r.Method(), URL: r.URL(),
			Headers: lowerHeaders(r.Headers()), Type: cdpType(r.ResourceType()),
		}
		if pd, err := r.PostData(); err == nil {
			ev.PostData = pd
		}
		sink.OnRequest(ev)
	})

	p.pg.OnResponse(func(resp pw.Response) {
		if stopped.Load() {
			return
		}
		rt := ""
		if req := resp.Request(); req != nil {
			rt = cdpType(req.ResourceType())
		}
		ev := browser.ResponseEvent{
			ID: responseID(resp), At: time.Now().UTC(), URL: resp.URL(),
			Status: resp.Status(), Headers: lowerHeaders(resp.Headers()),
			MimeType: mimeOf(resp), Type: rt,
		}
		// Skip bodies for redirects; cap size; tolerate "body unavailable".
		if resp.Status() < 300 || resp.Status() >= 400 {
			if body, err := resp.Body(); err == nil && len(body) < 512*1024 {
				ev.Body = string(body)
			}
		}
		sink.OnResponse(ev)
	})

	return func() { stopped.Store(true) }
}

// ExportState captures cookies plus the current origin's local/session storage.
func (p *cfPage) ExportState(ctx context.Context) (browser.SessionState, error) {
	st := browser.SessionState{Origin: p.Origin(), CapturedAt: time.Now().UTC(), UserAgent: p.b.UserAgent(),
		LocalStorage: map[string]string{}, SessionStorage: map[string]string{}}
	cookies, err := p.bctx.Cookies()
	if err != nil {
		return st, fmt.Errorf("camoufox: get cookies: %w", err)
	}
	for _, c := range cookies {
		st.Cookies = append(st.Cookies, browser.Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: c.Expires, HTTPOnly: c.HttpOnly, Secure: c.Secure, SameSite: sameSiteStr(c.SameSite),
		})
	}
	for _, kind := range []string{"localStorage", "sessionStorage"} {
		v, err := p.pg.Evaluate(`(k) => { const o = {}; const s = window[k]; for (let i = 0; i < s.length; i++) { const key = s.key(i); o[key] = s.getItem(key); } return JSON.stringify(o); }`, kind)
		if err != nil {
			continue
		}
		s, _ := v.(string)
		m := map[string]string{}
		_ = json.Unmarshal([]byte(s), &m)
		if kind == "localStorage" {
			st.LocalStorage = m
		} else {
			st.SessionStorage = m
		}
	}
	return st, nil
}

// ImportState restores cookies into the context, seeds local/sessionStorage via
// a per-origin init script that runs BEFORE page scripts (so the first load sees
// the restored storage), then navigates to the origin.
func (p *cfPage) ImportState(ctx context.Context, st browser.SessionState) error {
	if len(st.Cookies) > 0 {
		cookies := make([]pw.OptionalCookie, 0, len(st.Cookies))
		for _, c := range st.Cookies {
			oc := pw.OptionalCookie{Name: c.Name, Value: c.Value}
			if c.Domain != "" {
				d, pth := c.Domain, c.Path
				oc.Domain, oc.Path = &d, &pth
			} else {
				u := st.Origin
				oc.URL = &u
			}
			if c.Expires > 0 {
				e := c.Expires
				oc.Expires = &e
			}
			ho, sec := c.HTTPOnly, c.Secure
			oc.HttpOnly, oc.Secure = &ho, &sec
			if ss := sameSiteAttr(c.SameSite); ss != nil {
				oc.SameSite = ss
			}
			cookies = append(cookies, oc)
		}
		if err := p.bctx.AddCookies(cookies); err != nil {
			return fmt.Errorf("camoufox: set cookies: %w", err)
		}
	}
	if st.Origin != "" && (len(st.LocalStorage) > 0 || len(st.SessionStorage) > 0) {
		local, _ := json.Marshal(st.LocalStorage)
		session, _ := json.Marshal(st.SessionStorage)
		seed := fmt.Sprintf(`() => {
  try {
    if (location.origin !== %q) return;
    const seed = (store, data) => { const o = JSON.parse(data); for (const k in o) store.setItem(k, o[k]); };
    seed(window.localStorage, %q);
    seed(window.sessionStorage, %q);
  } catch (e) {}
}`, st.Origin, string(local), string(session))
		if err := p.bctx.AddInitScript(pw.Script{Content: pw.String(seed)}); err != nil {
			return fmt.Errorf("camoufox: seed storage: %w", err)
		}
		if _, err := p.pg.Goto(st.Origin+"/", pw.PageGotoOptions{WaitUntil: pw.WaitUntilStateLoad, Timeout: timeoutMs(ctx)}); err != nil {
			return fmt.Errorf("camoufox: navigate origin: %w", err)
		}
	}
	return nil
}

// requestID / responseID derive a stable per-request id. Playwright has no
// CDP-style request id, so the request pointer's GUID stands in; request and its
// response share it so the recorder can pair them.
func requestID(r pw.Request) string   { return fmt.Sprintf("%p", r) }
func responseID(r pw.Response) string { return fmt.Sprintf("%p", r.Request()) }

func mimeOf(resp pw.Response) string {
	h := lowerHeaders(resp.Headers())
	ct := h["content-type"]
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}

func sameSiteStr(s *pw.SameSiteAttribute) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

func sameSiteAttr(s string) *pw.SameSiteAttribute {
	switch strings.ToLower(s) {
	case "strict":
		return pw.SameSiteAttributeStrict
	case "lax":
		return pw.SameSiteAttributeLax
	case "none":
		return pw.SameSiteAttributeNone
	}
	return nil
}
