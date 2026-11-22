package rodbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"

	"github.com/creativetoken/chromancer/internal/browser"
)

// rodValue wraps a gson.JSON result as a neutral browser.Value.
type rodValue struct{ j gson.JSON }

func (v rodValue) Str() string    { return v.j.Str() }
func (v rodValue) Bool() bool     { return v.j.Bool() }
func (v rodValue) Int() int       { return v.j.Int() }
func (v rodValue) Float() float64 { return v.j.Num() }
func (v rodValue) Nil() bool      { return v.j.Nil() }
func (v rodValue) Unmarshal(x any) error {
	b, err := json.Marshal(v.j.Val())
	if err != nil {
		return err
	}
	return json.Unmarshal(b, x)
}

// rodFrame wraps a rod page (a frame: the main document or an iframe).
type rodFrame struct{ fr *rod.Page }

func (f *rodFrame) Eval(ctx context.Context, js string, args ...any) (browser.Value, error) {
	res, err := f.fr.Context(ctx).Eval(js, args...)
	if err != nil {
		return nil, fmt.Errorf("browser: frame eval: %w", err)
	}
	return rodValue{j: res.Value}, nil
}

// rodElement wraps a rod element handle.
type rodElement struct{ el *rod.Element }

func asEl(e browser.Element) (*rod.Element, error) {
	re, ok := e.(*rodElement)
	if !ok || re == nil || re.el == nil {
		return nil, fmt.Errorf("browser: foreign or nil element handle %T", e)
	}
	return re.el, nil
}

// rodPage is a single tab.
type rodPage struct {
	rp *rod.Page
	b  *rodBrowser
}

var _ browser.Page = (*rodPage)(nil)

// Navigate loads url and waits for the load event, then for the SPA to settle.
func (p *rodPage) Navigate(ctx context.Context, url string) error {
	rp := p.rp.Context(ctx)
	if err := rp.Navigate(url); err != nil {
		return fmt.Errorf("browser: navigate %s: %w", url, err)
	}
	if err := rp.WaitLoad(); err != nil && !errors.Is(err, context.Canceled) {
		// Some pages never fire load under a blocked subresource; continue.
		p.b.logf("waitload: %v", err)
	}
	return p.WaitStable(ctx, browser.DefaultStableTimeout)
}

// Reload reloads the current page and waits for it to settle. Useful for the
// post-login capture window so authenticated XHRs re-fire even on a SPA whose
// landing route is a hash change (which Navigate would not reload).
func (p *rodPage) Reload(ctx context.Context) error {
	rp := p.rp.Context(ctx)
	if err := rp.Reload(); err != nil {
		return fmt.Errorf("browser: reload: %w", err)
	}
	if err := rp.WaitLoad(); err != nil && !errors.Is(err, context.Canceled) {
		p.b.logf("reload waitload: %v", err)
	}
	return p.WaitStable(ctx, browser.DefaultStableTimeout)
}

// URL returns the current top-level URL.
func (p *rodPage) URL() string {
	info, err := p.rp.Info()
	if err != nil {
		return ""
	}
	return info.URL
}

// Title returns the document title.
func (p *rodPage) Title() string {
	res, err := p.rp.Eval(`() => document.title`)
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

// WaitStable waits for DOM mutations to settle and in-flight requests to go
// idle, bounded by timeout. It never returns an error for a mere timeout —
// a page that keeps polling is still "stable enough" to act on.
func (p *rodPage) WaitStable(ctx context.Context, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rp := p.rp.Context(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rp.Timeout(timeout).WaitStable(300 * time.Millisecond)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout + time.Second):
	}
	// Give any in-flight XHR a moment to land (bounded).
	wait := rp.Timeout(2*time.Second).WaitRequestIdle(300*time.Millisecond, nil, nil, nil)
	wait()
	return nil
}

// Eval runs a JS function (e.g. `() => document.title`) in the main frame.
func (p *rodPage) Eval(ctx context.Context, js string, args ...any) (browser.Value, error) {
	res, err := p.rp.Context(ctx).Eval(js, args...)
	if err != nil {
		return nil, fmt.Errorf("browser: eval: %w", err)
	}
	return rodValue{j: res.Value}, nil
}

// Frames returns the page plus every nested iframe (depth 1), so perception and
// actions can target elements inside frames. Index 0 is the main frame.
func (p *rodPage) Frames(ctx context.Context) ([]browser.Frame, error) {
	rp := p.rp.Context(ctx)
	out := []browser.Frame{&rodFrame{fr: rp}}
	els, err := rp.Timeout(2 * time.Second).Elements("iframe")
	if err != nil {
		return out, nil // no iframes
	}
	for _, el := range els {
		fr, err := el.Frame()
		if err != nil {
			continue
		}
		out = append(out, &rodFrame{fr: fr.Context(ctx)})
	}
	return out, nil
}

// Screenshot captures the viewport as PNG.
func (p *rodPage) Screenshot(ctx context.Context) ([]byte, error) {
	data, err := p.rp.Context(ctx).Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return nil, fmt.Errorf("browser: screenshot: %w", err)
	}
	return data, nil
}

// Element finds an element by CSS selector in the given frame (nil = main),
// waiting up to timeout.
func (p *rodPage) Element(ctx context.Context, frame browser.Frame, selector string, timeout time.Duration) (browser.Element, error) {
	target := p.rp
	if frame != nil {
		rf, ok := frame.(*rodFrame)
		if !ok || rf == nil || rf.fr == nil {
			return nil, fmt.Errorf("browser: foreign or nil frame handle %T", frame)
		}
		target = rf.fr
	}
	el, err := target.Context(ctx).Timeout(timeout).Element(selector)
	if err != nil {
		return nil, fmt.Errorf("browser: element %q: %w", selector, err)
	}
	return &rodElement{el: el}, nil
}

// WaitElementReady waits for an element to become genuinely actionable —
// enabled, its box stable (not mid-animation or mid-hydration), and hit-testable
// — bounded by the element's context. DOM-mutation quiet alone (WaitStable) is a
// weak proxy for "this SPA is interactive": a slow client-rendered page exposes a
// skeleton control during a pre-hydration lull, so a click can land before the
// handler is attached. Gating the click on real readiness closes that race; a
// detached/never-settling element returns an error so the caller can re-perceive
// a fresh handle.
func (p *rodPage) WaitElementReady(ctx context.Context, e browser.Element, stableFor time.Duration) error {
	el, err := asEl(e)
	if err != nil {
		return err
	}
	el = el.Context(ctx)
	if err := el.WaitEnabled(); err != nil {
		return fmt.Errorf("browser: wait enabled: %w", err)
	}
	if stableFor > 0 {
		if err := el.WaitStable(stableFor); err != nil {
			return fmt.Errorf("browser: wait stable: %w", err)
		}
	}
	if _, err := el.WaitInteractable(); err != nil {
		return fmt.Errorf("browser: wait interactable: %w", err)
	}
	return nil
}

// Click clicks an element.
func (p *rodPage) Click(ctx context.Context, e browser.Element) error {
	el, err := asEl(e)
	if err != nil {
		return err
	}
	el = el.Context(ctx)
	_ = el.ScrollIntoView()
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		// Fallback: JS click for elements that resist synthetic input.
		if _, jerr := el.Eval(`() => this.click()`); jerr != nil {
			return fmt.Errorf("browser: click: %w", err)
		}
	}
	return nil
}

// ClickAt clicks at viewport coordinates (used for set-of-marks region clicks).
func (p *rodPage) ClickAt(ctx context.Context, x, y int) error {
	rp := p.rp.Context(ctx)
	if err := rp.Mouse.MoveTo(proto.NewPoint(float64(x), float64(y))); err != nil {
		return fmt.Errorf("browser: move: %w", err)
	}
	if err := rp.Mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("browser: click at: %w", err)
	}
	return nil
}

// TypeInto clears an input and types text into it (human-like via rod's Input
// which dispatches key events).
func (p *rodPage) TypeInto(ctx context.Context, e browser.Element, text string) error {
	el, err := asEl(e)
	if err != nil {
		return err
	}
	el = el.Context(ctx)
	_ = el.ScrollIntoView()
	// Normal path: programmatic focus (rod waits for interactability), clear, and
	// type real key events. This is what most inputs want.
	const attempt = 3 * time.Second
	fctx, cancelF := context.WithTimeout(ctx, attempt)
	if err := el.Context(fctx).Focus(); err == nil {
		_ = el.SelectAllText()
		_ = el.Type(input.Backspace)
		ictx, cancelI := context.WithTimeout(ctx, attempt)
		_ = el.Context(ictx).Input(text)
		cancelI()
	}
	cancelF()
	// Verify the value actually landed. Real key events silently fail to register
	// on guarded inputs (focus timed out, the field is readonly until clicked, or
	// a one-time-code field sits under a transient overlay). When the value did
	// not take, un-guard with a real click, then set it directly and fire the
	// input/change events a framework listens for. Everything is bounded so a
	// stuck field cannot hang the whole verification step.
	vctx, cancelV := context.WithTimeout(ctx, attempt)
	got, _ := el.Context(vctx).Eval(`() => this.value`)
	cancelV()
	if got != nil && got.Value.Str() == text {
		return nil
	}
	cctx, cancelC := context.WithTimeout(ctx, attempt)
	_ = el.Context(cctx).Click(proto.InputMouseButtonLeft, 1) // best effort
	cancelC()
	jctx, cancelJ := context.WithTimeout(ctx, attempt)
	defer cancelJ()
	_, jerr := el.Context(jctx).Eval(`(v) => {
		this.focus();
		this.value = "";
		this.value = v;
		this.dispatchEvent(new Event("input", { bubbles: true }));
		this.dispatchEvent(new Event("change", { bubbles: true }));
	}`, text)
	if jerr != nil {
		return fmt.Errorf("browser: input: %w", jerr)
	}
	return nil
}

// TypeText types text into whatever element currently holds focus, with no DOM
// handle. Visual/coordinate mode uses it after ClickAt focuses a field that is
// not perceivable (closed shadow root, canvas, cross-origin iframe). InsertText
// dispatches an input event so framework-controlled inputs update.
func (p *rodPage) TypeText(ctx context.Context, text string) error {
	rp := p.rp.Context(ctx)
	if err := rp.InsertText(text); err != nil {
		return fmt.Errorf("browser: type text: %w", err)
	}
	return nil
}

// SelectOption selects an <option> by visible text or value.
func (p *rodPage) SelectOption(ctx context.Context, e browser.Element, value string) error {
	el, err := asEl(e)
	if err != nil {
		return err
	}
	el = el.Context(ctx)
	if err := el.Select([]string{value}, true, rod.SelectorTypeText); err != nil {
		if err2 := el.Select([]string{value}, true, rod.SelectorTypeCSSSector); err2 != nil {
			return fmt.Errorf("browser: select %q: %w", value, err)
		}
	}
	return nil
}

// PressEnter submits via the keyboard on the focused element.
func (p *rodPage) PressEnter(ctx context.Context, e browser.Element) error {
	el, err := asEl(e)
	if err != nil {
		return err
	}
	el = el.Context(ctx)
	if err := el.Focus(); err != nil {
		return err
	}
	return el.Type(input.Enter)
}

// ExpectPopup runs trigger and returns the new page it opens (login/magic-link
// popups), bounded by timeout. The new tab is set up like NewPage (UA + Network).
func (p *rodPage) ExpectPopup(ctx context.Context, trigger func() error, timeout time.Duration) (browser.Page, error) {
	// WaitOpen is bound to the parent page's own (long-lived) context; the
	// timeout is gated with a timer so the popup we return is NOT tied to a ctx
	// that gets cancelled when this call returns.
	wait := p.rp.WaitOpen()
	if err := trigger(); err != nil {
		return nil, err
	}
	type res struct {
		pg  *rod.Page
		err error
	}
	ch := make(chan res, 1)
	go func() {
		np, err := wait()
		ch <- res{np, err}
	}()
	select {
	case <-time.After(timeout):
		return nil, fmt.Errorf("browser: expect popup: timed out after %s", timeout)
	case <-ctx.Done():
		return nil, fmt.Errorf("browser: expect popup: %w", ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("browser: expect popup: %w", r.err)
		}
		if r.pg == nil {
			return nil, fmt.Errorf("browser: expect popup: no page opened")
		}
		_ = p.b.applyUA(r.pg)
		_ = (proto.NetworkEnable{}).Call(r.pg)
		return &rodPage{rp: r.pg.Context(ctx), b: p.b}, nil
	}
}

// Back navigates history back.
func (p *rodPage) Back(ctx context.Context) error {
	if err := p.rp.Context(ctx).NavigateBack(); err != nil {
		return fmt.Errorf("browser: back: %w", err)
	}
	return p.WaitStable(ctx, browser.DefaultStableTimeout)
}

// Close closes the tab.
func (p *rodPage) Close() error { return p.rp.Close() }

// Origin returns scheme://host[:port] of the current URL.
func (p *rodPage) Origin() string {
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
