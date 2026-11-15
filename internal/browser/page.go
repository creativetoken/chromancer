package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

// Page is a single tab. All methods respect the ctx the page was created with
// (and any ctx passed), so budgets/kill-switch unwind cleanly.
type Page struct {
	rp *rod.Page
	b  *Browser
}

// Rod exposes the underlying rod page.
func (p *Page) Rod() *rod.Page { return p.rp }

// Browser returns the owning browser.
func (p *Page) Browser() *Browser { return p.b }

// Navigate loads url and waits for the load event, then for the SPA to settle.
func (p *Page) Navigate(ctx context.Context, url string) error {
	rp := p.rp.Context(ctx)
	if err := rp.Navigate(url); err != nil {
		return fmt.Errorf("browser: navigate %s: %w", url, err)
	}
	if err := rp.WaitLoad(); err != nil && !errors.Is(err, context.Canceled) {
		// Some pages never fire load under a blocked subresource; continue.
		p.b.logf("waitload: %v", err)
	}
	return p.WaitStable(ctx, DefaultStableTimeout)
}

// URL returns the current top-level URL.
func (p *Page) URL() string {
	info, err := p.rp.Info()
	if err != nil {
		return ""
	}
	return info.URL
}

// Title returns the document title.
func (p *Page) Title() string {
	res, err := p.rp.Eval(`() => document.title`)
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

// WaitStable waits for DOM mutations to settle and in-flight requests to go
// idle, bounded by timeout. It never returns an error for a mere timeout —
// a page that keeps polling is still "stable enough" to act on.
func (p *Page) WaitStable(ctx context.Context, timeout time.Duration) error {
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
func (p *Page) Eval(ctx context.Context, js string, args ...any) (gson.JSON, error) {
	res, err := p.rp.Context(ctx).Eval(js, args...)
	if err != nil {
		return gson.JSON{}, fmt.Errorf("browser: eval: %w", err)
	}
	return res.Value, nil
}

// Frames returns the page plus every nested iframe as rod pages (depth 1),
// so perception and actions can target elements inside frames. Index 0 is
// the main frame.
func (p *Page) Frames(ctx context.Context) ([]*rod.Page, error) {
	rp := p.rp.Context(ctx)
	out := []*rod.Page{rp}
	els, err := rp.Timeout(2 * time.Second).Elements("iframe")
	if err != nil {
		return out, nil // no iframes
	}
	for _, el := range els {
		fr, err := el.Frame()
		if err != nil {
			continue
		}
		out = append(out, fr.Context(ctx))
	}
	return out, nil
}

// Screenshot captures the viewport as PNG.
func (p *Page) Screenshot(ctx context.Context) ([]byte, error) {
	data, err := p.rp.Context(ctx).Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return nil, fmt.Errorf("browser: screenshot: %w", err)
	}
	return data, nil
}

// Element finds an element by CSS selector in the given frame (nil = main),
// waiting up to timeout.
func (p *Page) Element(ctx context.Context, frame *rod.Page, selector string, timeout time.Duration) (*rod.Element, error) {
	if frame == nil {
		frame = p.rp
	}
	el, err := frame.Context(ctx).Timeout(timeout).Element(selector)
	if err != nil {
		return nil, fmt.Errorf("browser: element %q: %w", selector, err)
	}
	return el, nil
}

// Click clicks an element.
func (p *Page) Click(ctx context.Context, el *rod.Element) error {
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

// TypeInto clears an input and types text into it (human-like via rod's
// Input which dispatches key events).
func (p *Page) TypeInto(ctx context.Context, el *rod.Element, text string) error {
	el = el.Context(ctx)
	_ = el.ScrollIntoView()
	if err := el.Focus(); err != nil {
		return fmt.Errorf("browser: focus: %w", err)
	}
	// Select-all + delete to clear existing content, then type.
	_ = el.SelectAllText()
	_ = el.Type(input.Backspace)
	if err := el.Input(text); err != nil {
		return fmt.Errorf("browser: input: %w", err)
	}
	return nil
}

// SelectOption selects an <option> by visible text or value.
func (p *Page) SelectOption(ctx context.Context, el *rod.Element, value string) error {
	el = el.Context(ctx)
	if err := el.Select([]string{value}, true, rod.SelectorTypeText); err != nil {
		if err2 := el.Select([]string{value}, true, rod.SelectorTypeCSSSector); err2 != nil {
			return fmt.Errorf("browser: select %q: %w", value, err)
		}
	}
	return nil
}

// PressEnter submits via the keyboard on the focused element.
func (p *Page) PressEnter(ctx context.Context, el *rod.Element) error {
	el = el.Context(ctx)
	if err := el.Focus(); err != nil {
		return err
	}
	return el.Type(input.Enter)
}

// Back navigates history back.
func (p *Page) Back(ctx context.Context) error {
	if err := p.rp.Context(ctx).NavigateBack(); err != nil {
		return fmt.Errorf("browser: back: %w", err)
	}
	return p.WaitStable(ctx, DefaultStableTimeout)
}

// Close closes the tab.
func (p *Page) Close() error { return p.rp.Close() }

// Origin returns scheme://host[:port] of the current URL.
func (p *Page) Origin() string {
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
