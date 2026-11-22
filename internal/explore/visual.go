package explore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// Visual mode: when the DOM can't be read (closed shadow root, canvas-drawn
// control, cross-origin iframe), the agent escalates to a viewport screenshot
// with set-of-marks numbers over the perceivable nodes, and acts by mark number
// or raw coordinate. It is an escalation off the cheap text path, not a
// replacement — see docs/DECISIONS.md.

func (e *Explorer) maxScreenshots() int {
	if e.MaxScreenshots > 0 {
		return e.MaxScreenshots
	}
	return 10
}

// visionAvailable reports whether visual mode can run: a vision model must be
// wired (VisionModel), and the driver must be in takeover mode (visual mode is
// an auth escalation, not a crawl feature).
func (e *Explorer) visionAvailable() bool {
	return e.takeoverMode() && e.VisionModel != nil
}

// wantVisual decides whether this step runs in visual mode. The text path is the
// default; visual engages when the cheap checks find no login affordance, the
// perceiver reports NeedsVision, or progress has stalled — and disengages as
// soon as a perceivable login form appears. The screenshot cap bounds cost.
func (e *Explorer) wantVisual(st perceive.State) bool {
	if !e.visionAvailable() {
		return false
	}
	if hasPerceivableLoginForm(st) {
		e.visualActive = false    // a real form is readable: back to the cheap path
		e.fillFoundNoForm = false // the form is readable now
		return false
	}
	if e.screenshotsUsed >= e.maxScreenshots() {
		return false
	}
	if e.visualActive {
		return true // stay visual until a form appears or budget runs out
	}
	// A visible canvas is an unambiguous "can't read this as text" signal (a
	// control painted on a canvas) at any phase.
	if hasVisibleCanvas(st) {
		e.visualActive = true
		return true
	}
	// The remaining triggers are for the FORM-FINDING phase only. Once a login
	// form has been filled (or an identifier submitted), a page with no readable
	// controls is a "check your email" / processing / interstitial page that the
	// text path owns (enter_verification, a trust/terms button) — NOT a hidden
	// form. Escalating there would hijack the flow into a dead visual loop.
	if e.loginAttempted {
		// One post-login exception: the page clearly asks for a one-time code to
		// TYPE, but no textual input is perceivable (the code field is in a closed
		// shadow root / custom component). The text verification path has no handle
		// to enter, so escalate — the code is then entered by coordinate. Magic-link
		// "check your email for a link" copy is excluded so passwordless flows are
		// untouched.
		if asksForCodeNoField(st) {
			e.visualActive = true
			return true
		}
		return false
	}
	// Pre-login: no interactive nodes at all (a form hidden in a closed shadow
	// root), the fill tool just reported the form is unreadable, a stall, or no
	// sign-in affordance while hunting for the form.
	if len(st.Nodes) == 0 || e.fillFoundNoForm || e.noProgressStreak >= 2 || noSignInAffordance(st) {
		e.visualActive = true
		return true
	}
	return false
}

// hasVisibleCanvas reports whether the page has a visible <canvas> (a control may
// be painted on it, invisible to the DOM).
func hasVisibleCanvas(st perceive.State) bool {
	for _, n := range st.Nodes {
		if n.Tag == "canvas" && n.Visible {
			return true
		}
	}
	return false
}

// hasPerceivableLoginForm reports whether a password input is visible in the
// perceived DOM — the clearest sign the login form is readable without vision.
func hasPerceivableLoginForm(st perceive.State) bool {
	for _, n := range st.Nodes {
		if n.Visible && n.Tag == "input" && n.Type == "password" {
			return true
		}
	}
	return false
}

var (
	// codeEntryRe matches copy that asks the user to TYPE a one-time code (not a
	// link). Kept tight so it does not fire on generic account/security text.
	codeEntryRe = regexp.MustCompile(`(?i)(enter (the|your|a|this|that)? ?(6.?digit |security |verification )?code|verification code|one.?time (code|password)|security code|passcode|type (the|your) code)`)
	// linkCopyRe matches passwordless magic-link copy; its presence suppresses the
	// code-entry escalation (there is no field to type into, a link arrives OOB).
	linkCopyRe = regexp.MustCompile(`(?i)(magic link|click the (sign.?in |login )?link|sign.?in link|link to (sign|log) ?in|we (sent|emailed) you a link)`)
)

// asksForCodeNoField reports whether the page asks for a one-time code to type
// but exposes NO perceivable textual input to type it into (the code field is in
// a closed shadow root / custom component). It is the post-login visual-escalation
// trigger for coordinate-based code entry. Magic-link copy is excluded so the
// passwordless "check your email" path stays on the text/interstitial path.
func asksForCodeNoField(st perceive.State) bool {
	hay := strings.ToLower(st.Title + " " + strings.Join(st.Headings, " ") + " " + st.Text)
	if linkCopyRe.MatchString(hay) {
		return false
	}
	if !codeEntryRe.MatchString(hay) {
		return false
	}
	for _, n := range st.Nodes {
		if !n.Visible || n.Tag != "input" {
			continue
		}
		switch n.Type {
		case "", "text", "tel", "number":
			return false // a code-capable field is perceivable; the text path owns it
		}
	}
	return true
}

var signInWordRe = regexp.MustCompile(`(?i)(sign[ -]?in|log[ -]?in|sign[ -]?on)`)

// noSignInAffordance reports whether the page offers NO text-reachable way to
// reach sign-in: no visible sign-in/log-in text, no <a href> whose target looks
// like sign-in/login/account/auth, and no header account/menu/icon control. When
// all are absent, the text path has nothing to click and visual mode should take
// over.
func noSignInAffordance(st perceive.State) bool {
	hay := strings.ToLower(st.Text + " " + strings.Join(st.Headings, " "))
	if signInWordRe.MatchString(hay) {
		return false
	}
	for _, n := range st.Nodes {
		lbl := strings.ToLower(n.Name + " " + n.Text + " " + n.Pos)
		if signInWordRe.MatchString(lbl) {
			return false
		}
		if n.Tag == "a" {
			h := strings.ToLower(n.Href)
			if strings.Contains(h, "sign-in") || strings.Contains(h, "signin") ||
				strings.Contains(h, "login") || strings.Contains(h, "log-in") ||
				strings.Contains(h, "account") || strings.Contains(h, "auth") {
				return false
			}
		}
		// A named account/avatar/menu control, or any unlabeled icon in a header,
		// is a candidate the text path can open.
		if strings.Contains(lbl, "account") || strings.Contains(lbl, "avatar") ||
			strings.Contains(lbl, "icon (unlabeled)") || strings.Contains(lbl, "menu") {
			return false
		}
	}
	return true
}

// fillFoundNoForm reports whether a fill tool's outcome means the login/signup
// form was not readable in the DOM (no form / no fields found) — a direct signal
// to escalate to visual mode. It matches the agent fill callback's "no login
// form (password field) on this page" and the crawl-path "no visible email or
// password" phrasings, but not a fill that found the form and failed to type.
func fillFoundNoForm(outcome string) bool {
	o := strings.ToLower(outcome)
	return strings.Contains(o, "no login form") ||
		strings.Contains(o, "no signup form") ||
		strings.Contains(o, "no visible email or password") ||
		strings.Contains(o, "no password field") ||
		strings.Contains(o, "no visible fields")
}

// marksLoginDone reports whether an action actually advanced PAST the
// form-finding phase — so later interstitial/verification pages stay on the text
// path (no visual escalation). A login/signup form that was really filled, a
// credential actually typed, or a verification/resend step all qualify. Crucially,
// a fill that found no form (fill_error / "no visible email or password") does
// NOT: the form was never there, so visual escalation must remain available.
func marksLoginDone(a llmAction, outcome string) bool {
	switch a.Action {
	case "enter_verification", "resend_verification":
		return true
	}
	if !actAttemptsLogin(a) {
		return false
	}
	// A credential fill/type counts once it did NOT error. Typing a credential is
	// visually a no-op ("no visible change"), so progress can't be judged by a page
	// change (that would miss a passwordless email-first step and wrongly leave the
	// form-finding phase open on the "check your email" page); the absence of an
	// error is what distinguishes a real fill/type from one that found no form.
	lo := strings.ToLower(outcome)
	return !strings.Contains(lo, "error") && !strings.Contains(lo, "not available") && !strings.Contains(lo, "unknown")
}

// actAttemptsLogin reports whether an action is part of entering credentials —
// used to mark the end of the form-finding phase (after which visual escalation
// on stall/no-affordance is suppressed).
func actAttemptsLogin(a llmAction) bool {
	switch a.Action {
	case "fill_login_form", "fill_signup_form":
		return true
	case "type", "type_text":
		// Any credential/identifier placeholder ends the form-finding phase,
		// including passwordless flows (magic link / email-only) that submit just
		// an email and then wait on a "check your email" page.
		return strings.Contains(a.Text, "{{account.")
	}
	return false
}

// isNoProgress reports whether an action's outcome means the agent is stuck, so
// visual mode can escalate after a couple of stalled steps. Navigations, shown
// messages, and successful fills/verifications are progress; no-ops, "no fields"
// fills, and errors are not.
func isNoProgress(action, outcome string) bool {
	switch action {
	case "note", "wait", "look":
		return false
	}
	o := strings.ToLower(outcome)
	switch {
	case strings.Contains(o, "navigated"), strings.Contains(o, "message shown"),
		strings.Contains(o, "entered"), strings.Contains(o, "verification:"),
		strings.Contains(o, "banner:"), strings.Contains(o, "logged in:"),
		strings.Contains(o, "login form: filled"), strings.Contains(o, "took a screenshot"):
		return false
	case o == "no visible change", strings.Contains(o, "error"),
		strings.Contains(o, "no visible email or password"),
		strings.Contains(o, "no element"), strings.Contains(o, "not logged in"):
		return true
	}
	return false
}

// decideStep chooses the next action. In visual mode it attaches a set-of-marks
// screenshot to the message and routes to the vision model; otherwise it uses
// the text path.
func (e *Explorer) decideStep(ctx context.Context, obs string, st perceive.State) (llmAction, *model.Usage, bool) {
	if !e.wantVisual(st) {
		a, u := e.decide(ctx, obs)
		return a, u, false
	}
	a, u := e.decideVisual(ctx, obs, st)
	return a, u, true
}

// decideVisual captures a viewport screenshot, overlays set-of-marks numbers for
// the perceived nodes, and asks the vision model for one action. On any capture
// failure it falls back to the text path so a step is never lost.
func (e *Explorer) decideVisual(ctx context.Context, obs string, st perceive.State) (llmAction, *model.Usage) {
	shot, err := e.Page.Screenshot(ctx)
	if err != nil {
		e.crawlDebug("visual: screenshot failed: %v", err)
		return e.decide(ctx, obs)
	}
	dpr := e.devicePixelRatio(ctx)
	marked, handles, err := drawSetOfMarks(shot, st.Nodes, dpr, 1200)
	if err != nil {
		e.crawlDebug("visual: set-of-marks failed: %v", err)
		marked = shot
	}
	e.screenshotsUsed++
	// Derive the CSS viewport from the screenshot's own device dimensions ÷ DPR,
	// rather than a window.innerWidth Eval (which returns 0 on some backends).
	// The screenshot is viewport-only, so this is exact; DPR defaults to 1, which
	// is correct on a virtual display even if its Eval also fails.
	vw, vh := screenshotCSSDims(shot, dpr)
	msgs := []model.Message{
		{Role: model.System, Text: e.systemPrompt() + "\n\n" + visualSystemAddendum},
		{Role: model.User, Text: obs + "\n\n" + visualPreamble(handles, vw, vh), Images: [][]byte{marked}},
	}
	m := e.VisionModel
	if m == nil {
		m = e.Model
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, usage, err := m.Structured(cctx, msgs, e.buildSchema(), model.ChatOpts{MaxTokens: 1024})
	if err != nil {
		e.crawlDebug("visual: model error: %v", err)
		return llmAction{Action: "note", Why: "model error: " + err.Error()}, usage
	}
	a, ok := parseAction(raw)
	e.crawlDebug("visual raw=%s parsed=%+v ok=%v", strings.TrimSpace(string(raw)), a, ok)
	if !ok {
		return llmAction{Action: "note", Why: "unparseable action"}, usage
	}
	return a, usage
}

// devicePixelRatio reads the page's DPR so set-of-marks can scale CSS-px rects to
// the screenshot's device pixels. Defaults to 1 on any failure.
func (e *Explorer) devicePixelRatio(ctx context.Context) float64 {
	v, err := e.Page.Eval(ctx, "() => window.devicePixelRatio || 1")
	if err != nil {
		return 1
	}
	if f := v.Float(); f > 0 {
		return f
	}
	return 1
}

// screenshotCSSDims derives the CSS viewport size from the (viewport-only)
// screenshot's device dimensions ÷ DPR. This is exact and avoids a
// window.innerWidth Eval that some backends return 0 for; DPR defaults to 1.
func screenshotCSSDims(shot []byte, dpr float64) (int, int) {
	w, h := pngDims(shot)
	if dpr <= 0 {
		dpr = 1
	}
	return int(float64(w) / dpr), int(float64(h) / dpr)
}

// visualPreamble lists which mark numbers are present so the model knows the set
// it can click by number (regions with no number need click_at), and states the
// viewport size so coordinates map correctly.
func visualPreamble(handles []int, vw, vh int) string {
	vp := ""
	if vw > 0 && vh > 0 {
		vp = fmt.Sprintf(" The viewport is %dx%d CSS px; click_at(x,y) coordinates are in that space (top-left origin).", vw, vh)
	}
	if len(handles) == 0 {
		return "VISUAL MODE: the screenshot has NO numbered marks (no DOM nodes were " +
			"perceivable). Use click_at(x,y) and type_text to operate the controls you " +
			"see in the image; coordinates are viewport CSS pixels." + vp
	}
	var b strings.Builder
	b.WriteString("VISUAL MODE: the screenshot is overlaid with numbered marks for the ")
	b.WriteString("perceivable controls (")
	for i, h := range handles {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%d", h)
		if i >= 24 {
			b.WriteString(", …")
			break
		}
	}
	b.WriteString("). Click a marked control with click(id). For a control you can SEE in ")
	b.WriteString("the image but that has NO number (drawn on a canvas, or inside a closed ")
	b.WriteString("component), use click_at(x,y) in viewport CSS pixels, then type_text to ")
	b.WriteString("type into it.")
	b.WriteString(vp)
	return b.String()
}

// visualSystemAddendum is appended to the system prompt on visual steps.
const visualSystemAddendum = `You are now in VISUAL MODE because the page could not be read as structured text (its controls are likely drawn on a canvas or inside a closed component). You are given a screenshot of the viewport. Numbered marks label the controls that WERE readable; many controls you can see may have no number.

Rules for visual mode:
- To operate a NUMBERED control, use click(id) with its number.
- To operate a control you can see but that has NO number, use click_at(x,y) where x,y are viewport pixels from the top-left of the image.
- To enter text, first click (or click_at) the field to focus it, then type_text with the text. For credentials type the literal placeholders {{account.email}} / {{account.password}} — never real values.
- After acting, the next screenshot shows the effect; verify before continuing.
- Prefer a numbered control over a raw coordinate when both would work.`
