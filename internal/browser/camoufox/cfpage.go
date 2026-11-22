package camoufox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	pw "github.com/mxschmitt/playwright-go"

	"github.com/creativetoken/chromancer/internal/browser"
)

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

func (p *cfPage) Click(ctx context.Context, e browser.Element) error {
	el, err := asHandle(e)
	if err != nil {
		return err
	}
	_ = el.ScrollIntoViewIfNeeded()
	if err := p.run(ctx, func() error { return el.Click(pw.ElementHandleClickOptions{Timeout: timeoutMs(ctx)}) }); err != nil {
		return fmt.Errorf("camoufox: click: %w", err)
	}
	return nil
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
	_ = el.ScrollIntoViewIfNeeded()
	// Each step is bounded to a few seconds (like rodbackend's 3s per-step budget)
	// so a guarded field that makes one step block cannot consume the whole
	// verification budget before the fallback runs — that was the "type timed out"
	// symptom after the first camoufox OTP fix.
	short := func(fn func() error) error {
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return p.run(sctx, fn)
	}
	stepMs := float64(3000)
	force := true
	delay := float64(20)
	// Normal path (best-effort — errors swallowed; the fallback below recovers).
	_ = short(func() error { return el.Focus() })
	_ = short(func() error { return el.Fill("", pw.ElementHandleFillOptions{Force: &force, Timeout: &stepMs}) }) // clear
	_ = short(func() error {
		return el.Type(text, pw.ElementHandleTypeOptions{Delay: &delay, Timeout: &stepMs})
	})
	// Did the value land? Real key events silently fail on guarded inputs.
	var got string
	_ = short(func() error {
		v, verr := el.Evaluate("el => el.value")
		if s, ok := v.(string); ok {
			got = s
		}
		return verr
	})
	if got == text {
		return nil
	}
	// Fallback: un-guard with a real click, then set the value directly and fire
	// the input/change events a framework needs to register it.
	_ = short(func() error { return el.Click(pw.ElementHandleClickOptions{Force: &force, Timeout: &stepMs}) })
	var jerr error
	_ = short(func() error {
		// Use the element's NATIVE value setter so a React/Vue-controlled input
		// (which tracks its value through that setter) registers the change — a
		// plain `el.value = v` is silently reverted by such frameworks.
		_, jerr = el.Evaluate(`(el, v) => {
			el.focus();
			var proto = (el.tagName === 'TEXTAREA') ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
			var d = Object.getOwnPropertyDescriptor(proto, 'value');
			if (d && d.set) { d.set.call(el, ''); d.set.call(el, v); } else { el.value = ''; el.value = v; }
			el.dispatchEvent(new Event("input", { bubbles: true }));
			el.dispatchEvent(new Event("change", { bubbles: true }));
		}`, text)
		return jerr
	})
	if jerr != nil {
		return fmt.Errorf("camoufox: type: %w", jerr)
	}
	return nil
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
