package auth

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/explore"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/perceive"
	"github.com/creativetoken/chromancer/internal/verify"
)

//go:embed prompts/takeover_system.md
var takeoverSystemPrompt string

//go:embed prompts/auth_system.md
var authSystemPrompt string

// RecoveryMode selects what happens when the deterministic flow hits a dead end.
type RecoveryMode int

// Recovery modes. The zero value is RecoveryAgent: try the LLM agent (when one
// is wired) then a human, degrading to human-only when no agent model is set —
// which preserves the pre-takeover behaviour for callers that do not set Agent.
const (
	RecoveryAgent RecoveryMode = iota // agent → human → fail (default)
	RecoveryHuman                     // human → fail (no agent)
	RecoveryOff                       // fail immediately (no agent, no human)
)

// AuthDriver selects how the sign-in is driven.
type AuthDriver int

// Auth drivers. The zero value is the deterministic driver (entry-finder + flow
// handlers + resolve loop, with the agent as recovery). AuthDriverAgent makes
// the LLM agent the sole driver from the entry page.
const (
	AuthDriverDeterministic AuthDriver = iota
	AuthDriverAgent
)

// ParseAuthDriver maps a CLI/config value to an AuthDriver.
func ParseAuthDriver(s string) (AuthDriver, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "deterministic", "det":
		return AuthDriverDeterministic, true
	case "agent", "llm":
		return AuthDriverAgent, true
	}
	return AuthDriverDeterministic, false
}

// ParseRecoveryMode maps a CLI flag value to a RecoveryMode.
func ParseRecoveryMode(s string) (RecoveryMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "agent":
		return RecoveryAgent, true
	case "human":
		return RecoveryHuman, true
	case "off", "none":
		return RecoveryOff, true
	}
	return RecoveryAgent, false
}

// agentEligible reports whether a dead-end trigger should be handed to the LLM
// agent. Bot/block-page and captcha-exhausted dead ends never reach recover
// (they go straight to doHandoff), so only the adaptive triggers are listed.
func agentEligible(r captcha.BlockReason) bool {
	switch r {
	case captcha.ReasonStuck, captcha.ReasonUnknownFlow, captcha.ReasonVerifyTimeout:
		return true
	}
	return false
}

// credRejectRe matches a form alert that is a hard credentials rejection (wrong
// password / unknown account), which must never be recovered.
var credRejectRe = regexp.MustCompile(`(?i)(incorrect|invalid|wrong|(un)?recogni[sz]e[ds]?|not ?match|does ?n'?t match|do ?n'?t recogni[sz]e|bad)[^.]{0,24}(password|e-?mail|username|credential|login|account)|(password|e-?mail|username|credential|login)[^.]{0,24}(incorrect|invalid|wrong|(un)?recogni[sz]e[ds]?|not ?match)|incorrect password|wrong password|invalid (e-?mail|login|credentials)`)

// isCredentialsRejection reports whether a form alert is a bad-credentials wall.
func isCredentialsRejection(alert string) bool {
	if strings.TrimSpace(alert) == "" {
		return false
	}
	return credRejectRe.MatchString(alert)
}

// blockPageRe matches a bot-wall / block / rate-limit page. The agent must never
// retry or reload into one of these, so they go straight to a human.
var blockPageRe = regexp.MustCompile(`(?i)(verify (you(’|'|)re|you are) (a )?human|are you a (human|robot)|unusual traffic|automated (traffic|requests)|access denied|request blocked|you have been blocked|rate.?limit|too many requests|press (and hold|& hold)|complete the (security )?check|cloudflare)`)

// IsBlockPage reports whether a perceived page looks like a bot-wall / block /
// rate-limit page (as opposed to a merely unrecognised flow).
func IsBlockPage(st perceive.State) bool {
	hay := strings.ToLower(st.Title + " " + strings.Join(st.Headings, " ") + " " + st.Text)
	return blockPageRe.MatchString(hay)
}

// runAgent hands the live page to the LLM agent to finish the flow the
// deterministic automation could not. It reuses the shared explore driver (same
// deny-list, secret substitution, untrusted-DATA fencing, loop/step budgets) and
// wires the verification/captcha/success/human tools to the Authenticator's own
// machinery. Success is gated ONLY by check_logged_in — the model saying "done"
// is never enough.
func (a *Authenticator) runAgent(ctx context.Context, p browser.Page, rec *handback.Recorder, deps FlowDeps, mode Mode, reason string, since *time.Time) (bool, error) {
	if a.Agent == nil {
		return false, nil
	}
	a.reportRecovering(0, reason)
	a.trace.Add("agent takeover: triggered (%s)", reason)
	a.log("agent_takeover", map[string]any{"reason": reason, "result": "start"})

	// A bounded budget for the takeover, independent of the outer resolve loop.
	bud := core.NewBudget(core.Limits{Steps: 25, Time: 3 * time.Minute})
	tctx, cancel := bud.Context(ctx)
	defer cancel()

	step := 0
	ex := a.buildAgentExplorer(p, rec, deps, mode, since, bud,
		a.takeoverPrompt(mode, reason), takeoverSystemPrompt,
		func() { step++; a.reportRecovering(step, reason) })

	rep, cerr := ex.Crawl(tctx)
	a.recordAgentTrace(reason, rep)
	if rep != nil {
		a.trace.AgentSteps += rep.Steps
	}
	if cerr != nil {
		a.log("agent_takeover", map[string]any{"reason": reason, "result": "error", "error": cerr.Error()})
		return false, cerr
	}

	// Authoritative success gate.
	ok, ev := a.agentCheckLoggedIn(ctx, p, rec)
	stop := ""
	if rep != nil {
		stop = rep.StopReason
	}
	if ok {
		a.trace.SuccessEvidence = ev
		a.trace.Add("agent takeover: authenticated (%s)", strings.Join(ev, ", "))
		a.log("agent_takeover", map[string]any{"reason": reason, "result": "success", "stop": stop})
		return true, nil
	}
	a.log("agent_takeover", map[string]any{"reason": reason, "result": "failed", "stop": stop})
	return false, nil
}

// buildAgentExplorer wires an Explorer whose tools are backed by the
// Authenticator's own verification/captcha/success/form-fill machinery. It is
// shared by the recovery takeover (runAgent) and the agent-primary driver
// (runAgentPrimary); systemPrompt + operator select the role. onStep, if set,
// is called before each check_logged_in (for a recovering{step} surface).
// Success is gated ONLY by check_logged_in.
func (a *Authenticator) buildAgentExplorer(p browser.Page, rec *handback.Recorder, deps FlowDeps, mode Mode, since *time.Time, bud core.Budget, operator, systemPrompt string, onStep func()) *explore.Explorer {
	ex := &explore.Explorer{
		Page:         p,
		Perceiver:    a.Perceiver,
		Budget:       bud,
		Audit:        a.Audit,
		Driver:       explore.DriverLLM,
		Model:        a.Agent,
		VisionModel:  a.VisionAgent,
		Secrets:      a.agentSecrets(),
		Prompt:       operator,
		SystemPrompt: systemPrompt,
		MaxSteps:     25,
		CheckLoggedIn: func(c context.Context) (bool, []string) {
			if onStep != nil {
				onStep()
			}
			return a.agentCheckLoggedIn(c, p, rec)
		},
		RequestHuman: func(c context.Context, r string) error {
			// The agent asks for a human; the caller performs the actual handoff,
			// so this just records the intent.
			a.trace.Add("agent: requested human (%s)", r)
			return nil
		},
	}
	// Only advertise a tool when its backing resource exists.
	if a.Verifier != nil {
		ex.EnterVerification = func(c context.Context, n *perceive.Node, channel string, wantLink bool, x, y int) (string, error) {
			return a.agentEnterVerification(c, p, deps, n, channel, wantLink, x, y, since)
		}
		ex.ResendVerification = func(c context.Context, n *perceive.Node) error {
			return a.agentResend(c, p, deps, n, since)
		}
	}
	if a.Captcha != nil {
		ex.SolveCaptcha = func(c context.Context) (string, error) {
			return a.agentSolveCaptcha(c, p)
		}
	}
	// Form-fill + consent tools are always available (they fill via placeholders;
	// the real values never reach the model, and the model still decides submit).
	ex.FillLoginForm = func(c context.Context, n *perceive.Node) (string, error) {
		return a.agentFillLoginForm(c, p, deps, n)
	}
	ex.FillSignupForm = func(c context.Context, n *perceive.Node) (string, error) {
		return a.agentFillSignupForm(c, p, deps, n)
	}
	ex.DismissBanner = func(c context.Context) (string, error) {
		return a.agentDismissBanner(c, p, deps)
	}
	return ex
}

// runAgentPrimary drives the WHOLE sign-in with the LLM agent from the entry
// page: no deterministic entry-finder, flow handlers or resolve loop. It
// navigates to the entry URL, lets the agent fill/submit/verify/dismiss using
// the tools, and gates success on check_logged_in. Escalation is agent → human
// → failed; a bot-wall goes straight to a human (the agent is never pointed into
// one). A hard credentials rejection fails without recovery.
func (a *Authenticator) runAgentPrimary(ctx context.Context, p browser.Page, rec *handback.Recorder, mode Mode, hints Hints) error {
	if a.Agent == nil {
		return fmt.Errorf("%w: agent-primary auth needs an LLM provider", ErrFlowFailed)
	}
	a.codeSent = false
	since := time.Now()

	// Navigate to the entry URL. Prefer the operator hint; fall back to the target
	// root. The agent may click an on-page "Log in" link from there if needed.
	entryURL := firstNonEmptyStr(hints.LoginURL, a.Target)
	if mode == Signup {
		entryURL = firstNonEmptyStr(hints.SignupURL, hints.LoginURL, a.Target)
	}
	if err := p.Navigate(ctx, entryURL); err != nil {
		return fmt.Errorf("auth: navigate to entry %q: %w", entryURL, err)
	}
	_ = p.WaitStable(ctx, browser.DefaultStableTimeout)
	a.base = TakeBaseline(ctx, p)
	a.trace.Entrypoint = entryURL
	a.trace.EntrypointHow = "agent"

	// A bot-wall at the door goes straight to a human — never drive the agent into
	// it. (After a submit the same check runs again inside the loop's success
	// gate; here it covers a block page that greets the very first request.)
	if st, _, err := a.Perceiver.Perceive(ctx, p); err == nil && IsBlockPage(st) {
		a.trace.Add("agent-primary: bot-wall at entry → human")
		return a.escalateHuman(ctx, p, rec, captcha.ReasonBlockPage, fmt.Errorf("%w: bot-wall at entry", ErrUnknownFlow))
	}

	deps := FlowDeps{Page: p, Perceiver: a.Perceiver, Log: a.log, Trace: a.tracef, Timeout: a.Timeout, Captcha: a.captchaHook(p)}

	bud := core.NewBudget(core.Limits{Steps: 25, Time: 3 * time.Minute})
	tctx, cancel := bud.Context(ctx)
	defer cancel()

	a.log("agent_primary", map[string]any{"mode": mode.String(), "entry": entryURL, "result": "start"})
	step := 0
	ex := a.buildAgentExplorer(p, rec, deps, mode, &since, bud,
		a.primaryPrompt(mode, hints), authSystemPrompt,
		func() { step++; a.reportRecovering(step, "agent-primary") })

	rep, cerr := ex.Crawl(tctx)
	a.recordAgentTrace("agent-primary", rep)
	if rep != nil {
		a.trace.AgentSteps += rep.Steps
	}
	if cerr != nil {
		a.log("agent_primary", map[string]any{"result": "error", "error": cerr.Error()})
	}

	// Authoritative success gate.
	if ok, ev := a.agentCheckLoggedIn(ctx, p, rec); ok {
		a.trace.SuccessEvidence = ev
		a.trace.Add("agent-primary: authenticated (%s)", strings.Join(ev, ", "))
		a.log("agent_primary", map[string]any{"result": "success"})
		return nil
	}

	// The agent did not authenticate. If a credentials rejection is visible, fail
	// hard; a bot-wall or any other dead end escalates to a human, then fails.
	st, _, _ := a.Perceiver.Perceive(ctx, p)
	if f := Classify(st); f.HasAlert && isCredentialsRejection(f.AlertText) {
		a.log("agent_primary", map[string]any{"result": "credentials_rejected"})
		return fmt.Errorf("%w", ErrCredentialsRejected)
	}
	reason := captcha.ReasonUnknownFlow
	if IsBlockPage(st) {
		reason = captcha.ReasonBlockPage
	}
	a.log("agent_primary", map[string]any{"result": "failed"})
	return a.escalateHuman(ctx, p, rec, reason, fmt.Errorf("%w: agent did not authenticate", ErrAgentTakeoverFailed))
}

// escalateHuman hands off to a human when one is configured, confirming success
// afterward; with no human it returns the sentinel failure.
func (a *Authenticator) escalateHuman(ctx context.Context, p browser.Page, rec *handback.Recorder, reason captcha.BlockReason, sentinel error) error {
	if a.Recovery != RecoveryOff && a.Handoff != nil {
		if err := a.doHandoff(ctx, reason); err != nil {
			return err
		}
		// The human just finished in the browser. A login redirect and the auth
		// cookies it sets can lag the Enter keypress, so settle the page and RETRY
		// the deterministic success check for a few seconds rather than probing once
		// immediately (which failed to recognise a human-completed login).
		deadline := time.Now().Add(12 * time.Second)
		for {
			_ = p.WaitStable(ctx, browser.DefaultStableTimeout)
			if ok, ev := a.confirmAuthenticated(ctx, p, rec); ok {
				a.trace.SuccessEvidence = ev
				a.trace.Add("authenticated after human handoff: %s", strings.Join(ev, ", "))
				return nil
			}
			if ctx.Err() != nil || time.Now().After(deadline) {
				break
			}
			select {
			case <-time.After(1500 * time.Millisecond):
			case <-ctx.Done():
			}
		}
		// The strict deterministic signals didn't fire. The OPERATOR completed the
		// handoff and pressed Enter, so trust a page that is clearly PAST the login
		// flow — no visible password field and the URL has left the sign-in route
		// onto a different, non-auth page. (bambulab lands on its homepage after
		// login, whose search box trips the strict detector's "identifier present"
		// guard, and whose auth cookies did not register as strong evidence.)
		st := mustPerceive(ctx, a, p)
		f := Classify(st)
		if f.Password == nil && a.base.URL != "" && st.URL != a.base.URL && !authWordRe.MatchString(st.Title) {
			a.trace.SuccessEvidence = []string{"operator_confirmed:" + st.URL}
			a.trace.Add("authenticated after human handoff (operator-confirmed, off the login route): %s", st.URL)
			return nil
		}
		// Record what the page looked like (cookie NAMES only, never values) so the
		// failure — and why the strong signals missed — is diagnosable next time.
		cookieNames := ""
		if cur, err := p.ExportState(ctx); err == nil {
			var names []string
			for _, c := range cur.Cookies {
				names = append(names, c.Name)
			}
			cookieNames = strings.Join(names, ",")
		}
		a.trace.Add("post-handoff: not authenticated (url=%s title=%q visible_password=%v visible_identifier=%v cookies=[%s])",
			st.URL, st.Title, f.Password != nil, f.Identifier != nil, cookieNames)
	}
	return sentinel
}

func firstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// primaryPrompt is the operator instruction for the agent-primary driver: the
// goal, the account resources (yes/no only), and the entry URL.
func (a *Authenticator) primaryPrompt(mode Mode, hints Hints) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sign in to %s for this account (%s flow).\n\n", a.Target, mode.String())
	b.WriteString("Account resources available to you (yes/no only — you never see the values):\n")
	fmt.Fprintf(&b, "- password login: yes\n")
	fmt.Fprintf(&b, "- email verification: %v\n", yesno(a.hasChannel(verify.Email)))
	fmt.Fprintf(&b, "- SMS verification: %v\n", yesno(a.hasChannel(verify.SMS)))
	fmt.Fprintf(&b, "- authenticator (TOTP): %v\n\n", yesno(a.Creds.TOTPSeed != "" || a.hasChannel(verify.TOTP)))
	if h := firstNonEmptyStr(hints.LoginURL, hints.SignupURL); h != "" {
		fmt.Fprintf(&b, "You are starting on the sign-in page. ")
	}
	b.WriteString("Fill and submit the login form, clear any interstitial or verification step, then call check_logged_in to confirm. Do not guess URLs.")
	return b.String()
}

// agentEnterVerification fetches the current code/link from the configured
// channel and enters it, reusing doVerify (which honours since/claims/alias).
// It returns only an outcome label — never the code.
func (a *Authenticator) agentEnterVerification(ctx context.Context, p browser.Page, deps FlowDeps, n *perceive.Node, channel string, wantLink bool, x, y int, since *time.Time) (string, error) {
	st := mustPerceive(ctx, a, p)
	vs := DetectVerifyStep(st)
	if !vs.Needed {
		vs = VerifyStep{Needed: true, CodeField: n}
	}
	if n != nil && vs.CodeField == nil {
		vs.CodeField = n
	}
	if ch := verify.Channel(strings.ToLower(channel)); ch != "" {
		vs.Channel = ch
	}
	if vs.Channel == "" {
		vs.Channel = verify.Email
	}
	if wantLink {
		vs.LinkOnly = true
	}
	// Visual entry: no perceivable code field, but the agent (in visual mode)
	// supplied a coordinate for one it can SEE (a closed shadow root). Enter the
	// code by focus + type-into-focus instead of by a (non-existent) handle.
	if vs.CodeField == nil && !vs.LinkOnly && (x != 0 || y != 0) {
		vs.Needed = true
		vs.Visual = true
		vs.VisualX = x
		vs.VisualY = y
	}
	// The agent gets a full verification window, not the (possibly short)
	// VerifyTimeout that only exists to trigger the deterministic→agent handoff:
	// once the agent is driving the verification, the code must have time to
	// arrive and be entered (slower engines like Camoufox need the headroom).
	if err := a.doVerify(ctx, p, deps, vs, since, a.agentVerifyBudget()); err != nil {
		a.saveOTPDebug(ctx, p, "verify_failed: "+err.Error())
		return "", err
	}
	if vs.LinkOnly {
		return "verification link opened", nil
	}
	return "verification code entered", nil
}

// saveOTPDebug writes a diagnostic screenshot and a summary of the page's input
// fields to the account workspace when OTP entry fails, so the actual OTP widget
// (one input vs. multiple digit boxes, a custom component, an error message) can
// be inspected offline. Best-effort: it never affects the flow.
func (a *Authenticator) saveOTPDebug(ctx context.Context, p browser.Page, note string) {
	st := mustPerceive(ctx, a, p)
	var fields, clickables []string
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if !n.Visible {
			continue
		}
		if n.Tag == "input" {
			fields = append(fields, fmt.Sprintf("{type=%q mode=%q max=%d id=%q name=%q ac=%q val=%q}",
				n.Type, n.InputMode, n.MaxLength, n.ID, n.NameAttr, n.Autocomplete, n.Value))
		} else if n.Tag == "button" || n.Tag == "a" || n.Role == "button" {
			lbl := n.Name
			if lbl == "" {
				lbl = n.Text
			}
			clickables = append(clickables, fmt.Sprintf("{%s %q}", n.Tag, lbl))
		}
	}
	// Raw-DOM probe: does a send-code trigger exist AT ALL (even if the perceiver
	// can't see it)? Reports its tag + whether it would be perceivable.
	sendProbe := ""
	if v, err := p.Eval(ctx, `() => {
		const re = /\b(send|get|request|resend)\b[\s\S]{0,16}?\bcode\b/i;
		for (const el of document.querySelectorAll('*')) {
			if (el.children && el.children.length > 2) continue;
			const t = (el.innerText || el.textContent || '').trim();
			if (!t || t.length > 28 || !re.test(t)) continue;
			const tag = el.tagName.toLowerCase();
			const perceivable = tag === 'button' || tag === 'a' || el.getAttribute('role') === 'button' || el.hasAttribute('onclick');
			return tag + ' text=' + JSON.stringify(t) + ' perceivable=' + perceivable;
		}
		return 'none';
	}`); err == nil {
		sendProbe = v.Str()
	}
	shotPath := ""
	ws := os.Getenv("AGENTBROWSER_WORKSPACE")
	if ws != "" {
		if shot, err := p.Screenshot(ctx); err == nil {
			dir := filepath.Join(ws, "accounts", a.Account)
			_ = os.MkdirAll(dir, 0o700)
			fn := filepath.Join(dir, fmt.Sprintf("otp-debug-%d.png", time.Now().Unix()))
			if os.WriteFile(fn, shot, 0o600) == nil {
				shotPath = fn
			}
		}
	}
	a.trace.Add("otp-debug (%s): url=%s inputs=%s clickables=%s send_code_probe=[%s] screenshot=%s",
		note, st.URL, strings.Join(fields, " "), strings.Join(clickables, " "), sendProbe, shotPath)
	a.log("otp_debug", map[string]any{"note": note, "url": st.URL, "inputs": fields,
		"clickables": clickables, "send_code_probe": sendProbe, "screenshot": shotPath})
}

// probeVerifyMessage logs any visible error/status text on the verification screen
// after a submit (e.g. "incorrect code"), so the trace shows whether a submitted
// code was REJECTED by the site vs. the submit not firing at all. Best-effort; it
// never surfaces the code and never affects the flow.
func (a *Authenticator) probeVerifyMessage(ctx context.Context, p browser.Page) {
	v, err := p.Eval(ctx, `() => {
		const out = [];
		for (const el of document.querySelectorAll('[role="alert"], [aria-live="assertive"], [aria-live="polite"], .error, .err, .ant-form-item-explain-error, .ant-message, .el-message, [class*="error" i], [class*="invalid" i]')) {
			const t = (el.innerText || el.textContent || '').trim();
			if (t && t.length <= 140) out.push(t);
		}
		const body = (document.body && document.body.innerText) || '';
		const m = body.match(/[^\n]*\b(incorrect|invalid|expired|wrong|does not match|too many|try again|resend)\b[^\n]*/i);
		if (m) out.push(m[0].trim());
		return out.slice(0, 4).join(' | ').slice(0, 300);
	}`)
	if err != nil {
		return
	}
	if msg := strings.TrimSpace(v.Str()); msg != "" {
		a.trace.Add("verification: page message after submit: %q", msg)
		a.log("verify_message", map[string]any{"message": msg})
	}
}

// agentVerifyBudget is the verification window the agent gets for entering a
// code/link. It is at least 45s, and never shorter than the configured
// VerifyTimeout, so the short escalation-trigger timeout does not starve the
// agent's own verification step.
func (a *Authenticator) agentVerifyBudget() time.Duration {
	const min = 45 * time.Second
	if a.VerifyTimeout > min {
		return a.VerifyTimeout
	}
	return min
}

// agentResend clicks the page's resend control and advances the since window so
// the next fetch waits for a genuinely fresh code.
func (a *Authenticator) agentResend(ctx context.Context, p browser.Page, deps FlowDeps, n *perceive.Node, since *time.Time) error {
	target := n
	if target == nil {
		target = DetectVerifyStep(mustPerceive(ctx, a, p)).Resend
	}
	if target == nil {
		return fmt.Errorf("no resend control on this page")
	}
	// Capture the freshness window BEFORE the click: the resend triggers the
	// server to deliver the new code during the click, so a timestamp taken after
	// the click (which, on a slower backend, returns well after delivery) would
	// exclude the very code we just asked for.
	before := time.Now()
	if err := (actor{deps}).click(ctx, target, "resend"); err != nil {
		return err
	}
	*since = before
	_ = p.WaitStable(ctx, browser.DefaultStableTimeout)
	a.trace.Add("agent takeover: resent verification")
	return nil
}

// agentSolveCaptcha runs the existing captcha hook on the current page. It is
// idempotent: once a solve is pending (token injected, not yet settled), a
// repeat call does NOT re-run the solver chain — which would exhaust it and hand
// off — but tells the agent to submit. This matters with weaker models that
// re-call solve_captcha because the widget is still visible after a solve.
func (a *Authenticator) agentSolveCaptcha(ctx context.Context, p browser.Page) (string, error) {
	hook := a.captchaHook(p)
	if hook == nil {
		return "no captcha solver configured", nil
	}
	if a.pendingSolve != "" {
		return "captcha already solved — submit the form now", nil
	}
	if err := hook(ctx); err != nil {
		return "", err
	}
	return "captcha solved — submit the form now", nil
}

// agentCheckLoggedIn runs deterministic success detection — the only gate on a
// successful takeover.
func (a *Authenticator) agentCheckLoggedIn(ctx context.Context, p browser.Page, rec *handback.Recorder) (bool, []string) {
	st, _, err := a.Perceiver.Perceive(ctx, p)
	if err != nil {
		return false, nil
	}
	var xhrs []handback.HAREntry
	if rec != nil {
		xhrs = rec.PostLoginXHRs()
	}
	return SuccessDetector{}.IsAuthenticated(ctx, p, st, a.base, xhrs, true)
}

// confirmAuthenticated re-perceives and runs success detection once (used after
// a recovery that happened outside the resolve loop).
func (a *Authenticator) confirmAuthenticated(ctx context.Context, p browser.Page, rec *handback.Recorder) (bool, []string) {
	return a.agentCheckLoggedIn(ctx, p, rec)
}

// agentSecrets builds the placeholder-substitution values for the agent. They
// are never placed in the model context; the Executor substitutes them at type
// time.
func (a *Authenticator) agentSecrets() explore.Secrets {
	s := explore.Secrets{Email: a.Creds.UsernameOrEmail, Password: a.Creds.Password, Phone: a.Creds.Phone}
	if seed := strings.TrimSpace(a.Creds.TOTPSeed); seed != "" {
		s.TOTP = func() (string, error) {
			ps, err := verify.ParseSeed(seed)
			if err != nil {
				return "", err
			}
			return ps.CodeAt(time.Now())
		}
	}
	return s
}

// takeoverPrompt is the operator instruction substituted into the takeover
// system template. It states the goal, the trigger, the recent deterministic
// trace, and the available resources as yes/no only (never values).
func (a *Authenticator) takeoverPrompt(mode Mode, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are taking over a %s on %s that the deterministic automation could not finish.\n", mode.String(), a.Target)
	fmt.Fprintf(&b, "It stopped because: %s.\n\n", reason)
	b.WriteString("Account resources available to you (yes/no only — you never see the values):\n")
	fmt.Fprintf(&b, "- email verification: %v\n", yesno(a.hasChannel(verify.Email)))
	fmt.Fprintf(&b, "- SMS verification: %v\n", yesno(a.hasChannel(verify.SMS)))
	fmt.Fprintf(&b, "- authenticator (TOTP): %v\n\n", yesno(a.Creds.TOTPSeed != "" || a.hasChannel(verify.TOTP)))
	if steps := lastTrace(a.trace.Steps, 6); len(steps) > 0 {
		b.WriteString("What the deterministic flow already tried (most recent last):\n")
		for _, s := range steps {
			fmt.Fprintf(&b, "- %s\n", s)
		}
		b.WriteString("\n")
	}
	b.WriteString("Finish the sign-in using the on-page controls and your tools, then call check_logged_in to confirm. Do not reload the page or guess URLs.")
	return b.String()
}

// hasChannel reports whether the configured verifier can serve a channel for
// this account (a yes/no capability check, never a value).
func (a *Authenticator) hasChannel(ch verify.Channel) bool {
	return a.Verifier != nil && a.Verifier.Supports(ch, a.Account)
}

// recordAgentTrace folds the agent's run into the handback trace (trigger, each
// action+outcome, notes, token accounting). No secret ever appears here.
func (a *Authenticator) recordAgentTrace(reason string, rep *explore.Report) {
	if rep == nil {
		a.trace.AgentTakeover = append(a.trace.AgentTakeover, "trigger="+reason)
		return
	}
	a.trace.AgentTakeover = append(a.trace.AgentTakeover,
		fmt.Sprintf("trigger=%s steps=%d stop=%s screenshots=%d in_tokens=%d out_tokens=%d", reason, rep.Steps, rep.StopReason, rep.Screenshots, rep.InTokens, rep.OutTokens))
	if rep.Screenshots > 0 {
		a.trace.Screenshots += rep.Screenshots
	}
	a.trace.InTokens += rep.InTokens
	a.trace.OutTokens += rep.OutTokens
	for _, ts := range rep.Trace {
		line := fmt.Sprintf("%d %s → %s", ts.N, ts.Action, ts.Outcome)
		if ts.Why != "" {
			line += " (" + ts.Why + ")"
		}
		a.trace.AgentTakeover = append(a.trace.AgentTakeover, line)
	}
	for _, n := range rep.Notes {
		a.trace.AgentTakeover = append(a.trace.AgentTakeover, "note: "+n)
	}
	if len(rep.Denied) > 0 {
		a.trace.AgentTakeover = append(a.trace.AgentTakeover, fmt.Sprintf("denied account-safety actions: %d", len(rep.Denied)))
	}
}

// reportRecovering notifies the optional control-surface hook.
func (a *Authenticator) reportRecovering(step int, reason string) {
	if a.OnRecovering != nil {
		a.OnRecovering(step, reason)
	}
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func lastTrace(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// --- agent form-fill + consent tools ---------------------------------------
// These are the deterministic helpers exposed as tools the agent calls. They
// fill via actor.typeInto (placeholder path — the real credential never reaches
// the model), and leave the decision of when to submit to the model.

var (
	consentRejectRe = regexp.MustCompile(`(?i)(reject|decline|necessary only|only necessary|refuse|deny non)`)
	consentAcceptRe = regexp.MustCompile(`(?i)(accept|agree|got it|allow all|i understand|^ok$)`)
)

// agentFillLoginForm fills the current page's login form (identifier + password)
// without submitting. The model submits with a separate action.
func (a *Authenticator) agentFillLoginForm(ctx context.Context, p browser.Page, deps FlowDeps, _ *perceive.Node) (string, error) {
	st := mustPerceive(ctx, a, p)
	f := Classify(st)
	if f.Password == nil {
		return "", fmt.Errorf("no login form (password field) on this page")
	}
	act := actor{deps}
	if f.Identifier != nil && f.Identifier.Value == "" {
		if err := act.typeInto(ctx, f.Identifier, a.Creds.UsernameOrEmail, "identifier"); err != nil {
			return "", err
		}
	}
	if err := act.typeInto(ctx, f.Password, a.Creds.Password, "password"); err != nil {
		return "", err
	}
	return "filled username and password; submit when ready", nil
}

// agentFillSignupForm fills (and submits) the current page's signup form using
// the existing signup fill logic.
func (a *Authenticator) agentFillSignupForm(ctx context.Context, p browser.Page, deps FlowDeps, _ *perceive.Node) (string, error) {
	st := mustPerceive(ctx, a, p)
	if err := (SignupHandler{}).Run(ctx, st, a.Creds, deps); err != nil {
		return "", err
	}
	return "filled and submitted the signup form", nil
}

// agentDismissBanner dismisses a cookie/consent banner, preferring the
// privacy-preserving option (reject non-essential) over accept.
func (a *Authenticator) agentDismissBanner(ctx context.Context, p browser.Page, deps FlowDeps) (string, error) {
	st := mustPerceive(ctx, a, p)
	var reject, accept *perceive.Node
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if n.Tag != "button" && n.Role != "button" && n.Tag != "a" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(n.Name))
		if name == "" {
			continue
		}
		if reject == nil && consentRejectRe.MatchString(name) {
			reject = n
		}
		if accept == nil && consentAcceptRe.MatchString(name) {
			accept = n
		}
	}
	target, label := reject, "rejected non-essential cookies"
	if target == nil {
		target, label = accept, "accepted the cookie banner"
	}
	if target == nil {
		return "no cookie/consent banner found", nil
	}
	if err := (actor{deps}).click(ctx, target, "consent"); err != nil {
		return "", err
	}
	_ = p.WaitStable(ctx, browser.DefaultStableTimeout)
	return label, nil
}
