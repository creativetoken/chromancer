// Package auth is the single owner of getting a session: entrypoint discovery,
// login/signup flow handlers, deterministic success detection, session
// restore, and producing the handback.
package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// Mode selects login vs signup.
type Mode int

// Modes.
const (
	Login Mode = iota
	Signup
)

// String renders the mode.
func (m Mode) String() string {
	if m == Signup {
		return "signup"
	}
	return "login"
}

// Hints are optional operator-supplied entrypoint URLs.
type Hints struct {
	LoginURL  string
	SignupURL string
}

// Sentinel errors mapped to job failure reasons by the server.
var (
	ErrSSOOnly          = errors.New("sso_only")
	ErrUnknownFlow      = errors.New("unknown_flow")
	ErrFlowFailed       = errors.New("flow_failed")
	ErrStuck            = errors.New("stuck")
	ErrEntryNotFound    = errors.New("entrypoint_not_found")
	ErrNotAuthenticated = errors.New("not_authenticated")
	ErrHandoffTimeout   = errors.New("handoff_timeout")
	// ErrCredentialsRejected is a hard credentials rejection (wrong password /
	// unknown account) confirmed after a retry: never recovered via agent/human.
	ErrCredentialsRejected = errors.New("credentials_rejected")
	// ErrVerifyTimeout is a verification code/link that never arrived in time.
	ErrVerifyTimeout = errors.New("verify_timeout")
	// ErrAgentTakeoverFailed is returned when the agent takeover was attempted,
	// did not authenticate, and no human handoff is available.
	ErrAgentTakeoverFailed = errors.New("agent_takeover_failed")
)

var (
	identRe    = regexp.MustCompile(`(?i)(e-?mail|user(name)?|login|account|phone|identifier)`)
	submitRe   = regexp.MustCompile(`(?i)^(sign ?in|log ?in|login|continue|next|submit|enter|go|sign ?up|register|create account|get started|join|verify|confirm)\b`)
	ssoRe      = regexp.MustCompile(`(?i)(sign in with|continue with|log in with|login with|sign up with)\s+\w+`)
	authWordRe = regexp.MustCompile(`(?i)(sign ?in|log ?in|login|sign ?up|register|create (an )?account|members|welcome back)`)
)

// Fields is the classified set of form controls on a page.
type Fields struct {
	Identifier *perceive.Node
	Password   *perceive.Node
	Submit     *perceive.Node
	HasAlert   bool
	AlertText  string
	SSOButtons []perceive.Node
}

// Classify finds the identifier / password / submit controls in a state.
func Classify(st perceive.State) Fields {
	var f Fields
	var submitByType, submitByText *perceive.Node
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if !n.Visible || n.Disabled {
			continue
		}
		switch {
		case n.Tag == "input" && n.Type == "password":
			if f.Password == nil {
				f.Password = n
			}
		case n.Tag == "input" && (n.Type == "email" || n.Type == "text" || n.Type == "tel" || n.Type == "") && f.Identifier == nil:
			if n.Type == "email" || n.Autocomplete == "username" || n.Autocomplete == "email" ||
				identRe.MatchString(n.Name) || identRe.MatchString(n.NameAttr) || identRe.MatchString(n.ID) || identRe.MatchString(n.Placeholder) {
				f.Identifier = n
			}
		case n.Tag == "button" || (n.Tag == "input" && (n.Type == "submit" || n.Type == "button")) || n.Role == "button":
			label := n.Name
			if label == "" {
				label = n.Text
			}
			if ssoRe.MatchString(label) {
				f.SSOButtons = append(f.SSOButtons, *n)
				continue
			}
			if n.Type == "submit" {
				if submitByType == nil {
					submitByType = n
				}
			} else if submitByText == nil && submitRe.MatchString(strings.TrimSpace(label)) {
				submitByText = n
			}
		case n.Tag == "a" && ssoRe.MatchString(n.Name):
			f.SSOButtons = append(f.SSOButtons, *n)
		}
	}
	// Prefer a real type=submit control over a text-matched button (the latter
	// can be a modal opener like a header "Sign in" button).
	switch {
	case submitByType != nil:
		f.Submit = submitByType
	case submitByText != nil:
		f.Submit = submitByText
	}
	// Fallback identifier: the first visible text-like input when a password exists.
	if f.Identifier == nil && f.Password != nil {
		for i := range st.Nodes {
			n := &st.Nodes[i]
			if n.Visible && n.Tag == "input" && (n.Type == "text" || n.Type == "email" || n.Type == "") && n.Handle < f.Password.Handle {
				f.Identifier = n
				break
			}
		}
	}
	lower := strings.ToLower(st.Text)
	if strings.Contains(lower, "invalid") || strings.Contains(lower, "incorrect") || strings.Contains(lower, "wrong password") || strings.Contains(lower, "no account") || strings.Contains(lower, "try again") {
		f.HasAlert = true
		f.AlertText = firstMatchLine(st.Text, []string{"invalid", "incorrect", "wrong", "no account", "try again"})
	}
	return f
}

func firstMatchLine(text string, needles []string) string {
	for _, sent := range strings.Split(text, ".") {
		ls := strings.ToLower(sent)
		for _, n := range needles {
			if strings.Contains(ls, n) {
				return strings.TrimSpace(sent)
			}
		}
	}
	return ""
}

// IsAuthForm reports whether the state shows a usable login/signup form (or
// the first step of a multistep flow) and which variant it looks like.
func IsAuthForm(st perceive.State, mode Mode) (ok bool, variant string) {
	f := Classify(st)
	mentions := authWordRe.MatchString(st.Title) || authWordRe.MatchString(strings.Join(st.Headings, " ")) || authWordRe.MatchString(st.Text)
	switch {
	case f.Password != nil && f.Identifier != nil:
		return true, "form"
	case f.Password != nil:
		return true, "form"
	case f.Identifier != nil && f.Submit != nil && mentions:
		return true, "multistep"
	}
	return false, ""
}

// IsSSOOnly reports whether the page offers only third-party sign-in buttons.
func IsSSOOnly(st perceive.State) bool {
	f := Classify(st)
	return len(f.SSOButtons) > 0 && f.Identifier == nil && f.Password == nil
}

// FlowDeps is what a FlowHandler needs to act.
type FlowDeps struct {
	Page      browser.Page
	Perceiver perceive.Perceiver
	Log       func(kind string, payload map[string]any)
	Trace     func(format string, args ...any)
	// Captcha, if set, is called by a handler just before it submits a form: it
	// detects and resolves any CAPTCHA (injecting the token) or escalates to a
	// human. A no-op when no challenge is present.
	Captcha func(ctx context.Context) error
	Timeout time.Duration
}

// beforeSubmit runs the captcha hook if present.
func (d FlowDeps) beforeSubmit(ctx context.Context) error {
	if d.Captcha == nil {
		return nil
	}
	return d.Captcha(ctx)
}

// FlowHandler handles one login/signup variant. It owns the form submit.
type FlowHandler interface {
	Variant() string
	CanHandle(st perceive.State) bool
	Run(ctx context.Context, st perceive.State, creds identity.Credentials, d FlowDeps) error
}

// actor performs typed actions against perceived nodes. Each action is bounded
// by FlowDeps.Timeout so a stuck element fails fast rather than hanging on the
// run context.
type actor struct{ d FlowDeps }

func (a actor) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	to := a.d.Timeout
	if to <= 0 {
		to = 8 * time.Second
	}
	return context.WithTimeout(ctx, to)
}

func (a actor) typeInto(ctx context.Context, n *perceive.Node, text, label string) error {
	cctx, cancel := a.bound(ctx)
	defer cancel()
	fr, err := perceive.FrameOf(cctx, a.d.Page, *n)
	if err != nil {
		return err
	}
	el, err := a.d.Page.Element(cctx, fr, perceive.Selector(n.Handle), a.d.Timeout)
	if err != nil {
		return err
	}
	if err := a.d.Page.TypeInto(cctx, el, text); err != nil {
		return err
	}
	a.d.Log("action", map[string]any{"kind": "type", "handle": n.Handle, "field": label, "frame": n.Frame})
	return nil
}

func (a actor) click(ctx context.Context, n *perceive.Node, label string) error {
	cctx, cancel := a.bound(ctx)
	defer cancel()
	fr, err := perceive.FrameOf(cctx, a.d.Page, *n)
	if err != nil {
		return err
	}
	el, err := a.d.Page.Element(cctx, fr, perceive.Selector(n.Handle), a.d.Timeout)
	if err != nil {
		return err
	}
	if err := a.d.Page.Click(cctx, el); err != nil {
		return err
	}
	a.d.Log("action", map[string]any{"kind": "click", "handle": n.Handle, "label": label, "frame": n.Frame})
	return nil
}

// clickSubmit clicks a submit control after gating on readiness: the control
// must be enabled, box-stable, and hit-testable before the click, so a click
// never lands on a pre-hydration skeleton button whose handler is not yet
// attached. A control that never settles (or whose handle is stale) returns an
// error, which submitWithRetry turns into a settle + re-perceive + retry.
func (a actor) clickSubmit(ctx context.Context, n *perceive.Node) error {
	cctx, cancel := a.bound(ctx)
	defer cancel()
	fr, err := perceive.FrameOf(cctx, a.d.Page, *n)
	if err != nil {
		return err
	}
	el, err := a.d.Page.Element(cctx, fr, perceive.Selector(n.Handle), a.d.Timeout)
	if err != nil {
		return err
	}
	if err := a.d.Page.WaitElementReady(cctx, el, 250*time.Millisecond); err != nil {
		return err
	}
	if err := a.d.Page.Click(cctx, el); err != nil {
		return err
	}
	a.d.Log("action", map[string]any{"kind": "click", "handle": n.Handle, "label": "submit", "frame": n.Frame})
	return nil
}

// submitAfterCaptcha runs the pre-submit CAPTCHA hook and then submits. The
// hook can mutate the DOM — a human solving a challenge in the browser, or a
// token callback re-rendering or auto-submitting the form — which invalidates
// the element handles perceived before it (clicking the stale submit handle
// then times out). So it re-perceives and re-locates the submit control. If the
// form has already advanced past the login/signup form (the solve submitted
// it), it returns without clicking and lets the resolve loop take over.
func (a actor) submitAfterCaptcha(ctx context.Context, f Fields, fallback *perceive.Node) error {
	if err := a.d.beforeSubmit(ctx); err != nil {
		return err
	}
	if a.d.Perceiver != nil {
		if st2, _, err := a.d.Perceiver.Perceive(ctx, a.d.Page); err == nil {
			f2 := Classify(st2)
			if f2.Submit == nil && f2.Password == nil && f2.Identifier == nil {
				a.d.Trace("submit: form advanced after captcha; nothing to click")
				return nil
			}
			f = f2
			switch {
			case f2.Password != nil:
				fallback = f2.Password
			case f2.Identifier != nil:
				fallback = f2.Identifier
			}
		}
	}
	return a.submitWithRetry(ctx, f, fallback)
}

// submitWithRetry submits and, if the click is stranded (a slow SPA can present
// a skeleton submit button before hydration attaches its handler, so the click
// lands on a stale/not-yet-interactive element and times out), waits for the DOM
// to settle, re-perceives a fresh live submit control, and retries. This
// generalises the post-captcha re-perceive to any re-render that strands submit
// — hydration, a token callback, a late modal — not just a CAPTCHA solve.
func (a actor) submitWithRetry(ctx context.Context, f Fields, fallback *perceive.Node) error {
	const attempts = 2
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			a.d.Trace("submit: click stranded, settling + re-perceiving (attempt %d): %v", i+1, lastErr)
			_ = a.d.Page.WaitStable(ctx, browser.DefaultStableTimeout)
			if a.d.Perceiver != nil {
				if st, _, perr := a.d.Perceiver.Perceive(ctx, a.d.Page); perr == nil {
					f2 := Classify(st)
					// The form may have advanced already (the earlier click landed
					// after all, or the page navigated) → nothing more to click.
					if f2.Submit == nil && f2.Password == nil && f2.Identifier == nil {
						a.d.Trace("submit: form advanced; nothing to re-click")
						return nil
					}
					f = f2
					switch {
					case f2.Password != nil:
						fallback = f2.Password
					case f2.Identifier != nil:
						fallback = f2.Identifier
					}
				}
			}
		}
		lastErr = a.submit(ctx, f, fallback)
		if lastErr == nil || !isStrandedClick(lastErr) || ctx.Err() != nil {
			return lastErr
		}
	}
	return lastErr
}

// isStrandedClick reports whether a submit error is the kind a re-perceive +
// retry can recover: the submit control was not actionable in time (a skeleton
// still hydrating) or the handle went stale under a re-render.
func isStrandedClick(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, sub := range []string{
		"deadline exceeded", "wait enabled", "wait stable", "wait interactable",
		"browser: element", "not found", "detached", "stale", "cannot find",
	} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func (a actor) submit(ctx context.Context, f Fields, fallback *perceive.Node) error {
	if f.Submit != nil {
		return a.clickSubmit(ctx, f.Submit)
	}
	if fallback == nil {
		return fmt.Errorf("%w: no submit control", ErrFlowFailed)
	}
	fr, err := perceive.FrameOf(ctx, a.d.Page, *fallback)
	if err != nil {
		return err
	}
	el, err := a.d.Page.Element(ctx, fr, perceive.Selector(fallback.Handle), a.d.Timeout)
	if err != nil {
		return err
	}
	a.d.Log("action", map[string]any{"kind": "press_enter", "handle": fallback.Handle})
	return a.d.Page.PressEnter(ctx, el)
}

// FormHandler handles a single-page login form, including forms inside a
// modal or an iframe (perception is frame-aware).
type FormHandler struct{}

// Variant implements FlowHandler.
func (FormHandler) Variant() string { return "form" }

// CanHandle implements FlowHandler.
func (FormHandler) CanHandle(st perceive.State) bool {
	return Classify(st).Password != nil
}

// Run implements FlowHandler.
func (h FormHandler) Run(ctx context.Context, st perceive.State, creds identity.Credentials, d FlowDeps) error {
	f := Classify(st)
	if f.Password == nil {
		return fmt.Errorf("%w: no password field", ErrUnknownFlow)
	}
	a := actor{d}
	if f.Identifier != nil {
		if err := a.typeInto(ctx, f.Identifier, creds.UsernameOrEmail, "identifier"); err != nil {
			return fmt.Errorf("type identifier: %w", err)
		}
	}
	if err := a.typeInto(ctx, f.Password, creds.Password, "password"); err != nil {
		return fmt.Errorf("type password: %w", err)
	}
	d.Trace("form: filled identifier+password, submitting")
	if err := a.submitAfterCaptcha(ctx, f, f.Password); err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	return d.Page.WaitStable(ctx, browser.DefaultStableTimeout)
}

// MultiStepHandler handles identifier-first flows (email → next → password).
type MultiStepHandler struct{}

// Variant implements FlowHandler.
func (MultiStepHandler) Variant() string { return "multistep" }

// CanHandle implements FlowHandler.
func (MultiStepHandler) CanHandle(st perceive.State) bool {
	f := Classify(st)
	return f.Password == nil && f.Identifier != nil
}

// Run implements FlowHandler. It does the initial identifier (and password, if
// already shown) interaction and then hands back to the resolve loop, which
// drives any further step — a password view, a verification step (e.g. a
// magic-link email), or success.
func (h MultiStepHandler) Run(ctx context.Context, st perceive.State, creds identity.Credentials, d FlowDeps) error {
	a := actor{d}
	for step := 1; step <= 2; step++ {
		f := Classify(st)
		switch {
		case f.Password != nil:
			if f.Identifier != nil && f.Identifier.Value == "" {
				if err := a.typeInto(ctx, f.Identifier, creds.UsernameOrEmail, "identifier"); err != nil {
					return err
				}
			}
			if err := a.typeInto(ctx, f.Password, creds.Password, "password"); err != nil {
				return err
			}
			d.Trace("multistep: step %d password submitted", step)
			if err := a.submitAfterCaptcha(ctx, f, f.Password); err != nil {
				return err
			}
			return d.Page.WaitStable(ctx, browser.DefaultStableTimeout)
		case f.Identifier != nil:
			if err := a.typeInto(ctx, f.Identifier, creds.UsernameOrEmail, "identifier"); err != nil {
				return err
			}
			d.Trace("multistep: step %d identifier submitted", step)
			if err := a.submit(ctx, f, f.Identifier); err != nil {
				return err
			}
			if err := d.Page.WaitStable(ctx, browser.DefaultStableTimeout); err != nil {
				return err
			}
		default:
			// No identifier/password field: a verification step or success page
			// appeared (e.g. "check your email"). Let the resolve loop handle it.
			return nil
		}
		var err error
		st, _, err = d.Perceiver.Perceive(ctx, d.Page)
		if err != nil {
			return err
		}
	}
	return nil
}

// DefaultHandlers is the ordered handler set tried against a perceived form.
func DefaultHandlers() []FlowHandler {
	return []FlowHandler{FormHandler{}, MultiStepHandler{}}
}
