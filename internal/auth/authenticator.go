package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/auth/entry"
	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/explore"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/perceive"
	"github.com/creativetoken/chromancer/internal/verify"
)

// Authenticator is the single owner of obtaining a session using
// operator-provided credentials and producing the handback.
type Authenticator struct {
	Target     string
	Account    string
	Creds      identity.Credentials
	Profile    identity.Profile
	Perceiver  perceive.Perceiver
	Router     model.ModelRouter
	Sessions   SessionStore
	Writer     handback.ArtefactWriter
	Audit      audit.AuditLog
	Budget     core.Budget
	Capture    handback.CaptureWindow
	EntryCache string
	Handlers   []FlowHandler
	Verifier   verify.Provider // optional; enables verification steps
	Detectors  []captcha.Detector
	Captcha    captcha.Resolver      // optional; enables CAPTCHA handling
	Handoff    *captcha.HumanHandoff // optional; escalation for any dead end
	MaxSteps   int                   // post-submit resolution attempts (default 6)
	Timeout    time.Duration
	// VerifyTimeout bounds how long doVerify waits for a code/link before giving
	// up (and, with recovery, handing off to the agent). Default 90s.
	VerifyTimeout time.Duration

	// Agent, when set with Recovery != RecoveryOff, enables the LLM agent
	// takeover step: a dead end the deterministic flow cannot pass is handed to
	// the agent (same live page, reusing the existing verifier/captcha/success/
	// handoff tools) before falling back to a human.
	Agent explore.LLMModel
	// VisionAgent, when set, drives visual-mode steps (screenshot + set-of-marks)
	// for the agent driver. It must be a vision-capable provider. When nil, visual
	// escalation is unavailable and the agent stays on the text path.
	VisionAgent explore.LLMModel
	Recovery    RecoveryMode
	// AuthDriver selects deterministic (default) or agent-primary sign-in. In
	// agent-primary mode the LLM agent drives the whole flow from the entry page
	// and the deterministic entry-finder/handlers/resolve loop are not used.
	AuthDriver AuthDriver
	// OnRecovering, if set, is called when the agent takes over (step 0) and on
	// each subsequent agent step, so a control surface can expose a
	// recovering{step,reason} state. Optional.
	OnRecovering func(step int, reason string)

	trace          handback.Trace
	backend        browser.Capabilities // the engine this run used (recorded in trace + bundle)
	base           Baseline
	pendingSolve   string
	pendingPayload string // token/text of the pending solve (for the reached-backend check)
	codeSent       bool   // a "Send code" trigger has been clicked this run (don't re-click: respect the server cooldown)
}

// Run authenticates (target, account) in the given mode and returns the
// handback result. It restores a healthy saved session without logging in,
// else runs the login/signup flow, handles verification/CAPTCHA via the
// provided hooks, detects success, captures the post-login window, builds the
// SessionBundle, runs the replayability check and writes the artefacts.
func (a *Authenticator) Run(ctx context.Context, b browser.Browser, mode Mode, hints Hints) (handback.Result, error) {
	if a.Perceiver == nil {
		a.Perceiver = perceive.New()
	}
	if len(a.Handlers) == 0 {
		a.Handlers = DefaultHandlers()
	}
	if a.Timeout == 0 {
		a.Timeout = 8 * time.Second
	}
	if a.Capture.N == 0 {
		a.Capture = handback.CaptureWindow{N: 20, T: 10 * time.Second}
	}
	a.trace = handback.Trace{StartedAt: time.Now().UTC(), Timings: map[string]string{}}
	caps := b.Capabilities()
	a.trace.Backend, a.trace.BackendVersion = caps.Name, caps.Version
	a.backend = caps

	p, err := b.NewPage(ctx)
	if err != nil {
		return handback.Result{}, fmt.Errorf("auth: new page: %w", err)
	}
	defer p.Close()

	rec := handback.NewRecorder()
	if err := rec.Begin(ctx, p); err != nil {
		return handback.Result{}, fmt.Errorf("auth: recorder: %w", err)
	}

	// 1. Restore a healthy saved session.
	if restored, res, ok := a.tryRestore(ctx, p, rec); ok {
		return res, nil
	} else if restored {
		a.log("session", map[string]any{"restore": "stale"})
	}

	// 2/3. Run the flow (entrypoint discovery + fill + submit + verify + success).
	if err := a.authenticate(ctx, p, rec, mode, hints); err != nil {
		// Carry the partial trace out on failure: the agent_takeover step log is
		// how a failed run (especially a live one) is diagnosed.
		return handback.Result{Trace: a.trace}, err
	}

	// 6. Success → post-login capture → bundle → replay → write.
	return a.finish(ctx, p, rec)
}

func (a *Authenticator) tryRestore(ctx context.Context, p browser.Page, rec *handback.Recorder) (bool, handback.Result, bool) {
	if a.Sessions == nil {
		return false, handback.Result{}, false
	}
	saved, err := a.Sessions.Load(ctx)
	if err != nil {
		return false, handback.Result{}, false
	}
	if err := p.ImportState(ctx, saved.State); err != nil {
		return true, handback.Result{}, false
	}
	landing := saved.LandingURL
	if landing == "" {
		landing = saved.State.Origin + "/"
	}
	if err := p.Navigate(ctx, landing); err != nil {
		return true, handback.Result{}, false
	}
	st, _, err := a.Perceiver.Perceive(ctx, p)
	if err != nil {
		return true, handback.Result{}, false
	}
	base := Baseline{URL: ""}
	// Restored session: do NOT trust injected cookies/storage as proof (every
	// cookie looks "new" against the empty baseline). Require a server-confirmed
	// signal; otherwise treat the session as stale and run a real login.
	ok, ev := SuccessDetector{}.IsAuthenticated(ctx, p, st, base, rec.PostLoginXHRs(), false)
	if !ok {
		return true, handback.Result{}, false
	}
	a.trace.Restored = true
	a.trace.SuccessEvidence = ev
	a.trace.Add("restored healthy session, no login")
	res, err := a.capture(ctx, p, rec, landing)
	if err != nil {
		return true, handback.Result{}, false
	}
	return true, res, true
}

func (a *Authenticator) authenticate(ctx context.Context, p browser.Page, rec *handback.Recorder, mode Mode, hints Hints) error {
	// Agent-primary: the LLM agent drives the whole sign-in from the entry page;
	// the deterministic entry-finder/handlers/resolve loop below are skipped.
	if a.AuthDriver == AuthDriverAgent {
		return a.runAgentPrimary(ctx, p, rec, mode, hints)
	}
	// Flow start, captured BEFORE the first form submit. A verification code is
	// sent when the form is submitted (below), so this is the lower bound for
	// "code newer than the flow" — set it here, not in resolve (which runs after
	// the submit), or the just-sent code would be excluded.
	a.codeSent = false
	since := time.Now()
	root := a.Target
	res := &entry.Resolver{CacheDir: a.EntryCache}
	check := func(st perceive.State, m entry.Mode) (bool, string) {
		return IsAuthForm(st, Mode(m))
	}
	hintList := []string{hints.LoginURL}
	if mode == Signup {
		hintList = []string{hints.SignupURL, hints.LoginURL}
	}
	ep, err := res.Resolve(ctx, a.Target, root, entry.Mode(mode), hintList, entry.Deps{
		Page: p, Perceiver: a.Perceiver, Router: a.Router, Check: check,
		Trace: a.tracef, Timeout: a.Timeout,
	})
	if err != nil {
		// SSO-only check: visit the login hint / signup hint / root and, if any
		// offers only third-party sign-in, fail fast with sso_only.
		for _, u := range []string{hints.LoginURL, hints.SignupURL, root} {
			if u == "" {
				continue
			}
			if nerr := p.Navigate(ctx, u); nerr != nil {
				continue
			}
			if st, _, perr := a.Perceiver.Perceive(ctx, p); perr == nil && IsSSOOnly(st) {
				return fmt.Errorf("%w", ErrSSOOnly)
			}
		}
		return fmt.Errorf("%w: %v", ErrEntryNotFound, err)
	}
	a.trace.Entrypoint = ep.URL
	a.trace.EntrypointHow = ep.How

	st, _, err := a.Perceiver.Perceive(ctx, p)
	if err != nil {
		return fmt.Errorf("auth: perceive form: %w", err)
	}
	if IsSSOOnly(st) {
		return fmt.Errorf("%w", ErrSSOOnly)
	}
	a.base = TakeBaseline(ctx, p)

	deps := FlowDeps{Page: p, Perceiver: a.Perceiver, Log: a.log, Trace: a.tracef, Timeout: a.Timeout, Captcha: a.captchaHook(p)}
	var handler FlowHandler
	if mode == Signup {
		handler = SignupHandler{Profile: a.Profile}
	} else {
		handler = a.pickHandler(st)
	}
	if handler == nil {
		return fmt.Errorf("%w: no handler for perceived form", ErrUnknownFlow)
	}
	a.trace.FlowVariant = handler.Variant()
	a.log("flow", map[string]any{"variant": handler.Variant(), "mode": mode.String()})
	if err := handler.Run(ctx, st, a.Creds, deps); err != nil {
		// A submit the deterministic flow could not land — a slow SPA whose submit
		// control never settled, or a handle gone stale under hydration — is not a
		// hard failure: the credentials are already filled, so fall through to the
		// resolve loop, which re-perceives, retries the submit, and escalates to
		// the agent/human if it is still stranded. A non-submit error still aborts.
		if !isStrandedClick(err) {
			return err
		}
		a.trace.Add("initial submit stranded (%v) → resolve loop", err)
		a.log("flow", map[string]any{"submit_stranded": err.Error()})
	}
	return a.resolve(ctx, p, rec, deps, mode, since)
}

// resolve is the post-submit loop: success | verification | account-exists →
// login | form-error recovery | SSO-only | stuck. since is the flow-start time
// (captured by authenticate before the first submit): verification only accepts
// a code/link newer than it, so a stale code from an earlier attempt is never
// grabbed while the code this flow just triggered still qualifies.
func (a *Authenticator) resolve(ctx context.Context, p browser.Page, rec *handback.Recorder, deps FlowDeps, mode Mode, since time.Time) error {
	maxSteps := a.MaxSteps
	if maxSteps == 0 {
		maxSteps = 6
	}
	formErrRetries := 0
	verified := false
	handoffUsed := false
	agentUsed := false
	agentAuthed := false
	actedHash := map[perceive.StateHash]int{}
	// recover hands a dead end (stuck / unknown flow / verify timeout) to the LLM
	// agent first (when one is configured and the trigger is eligible), then to a
	// human, then returns the sentinel error. A hard credentials rejection is
	// never recovered. On a successful recovery it clears actedHash so the form
	// gets a fresh chance and returns nil.
	recoverDeadEnd := func(reason captcha.BlockReason, alertText string, sentinel error) error {
		if ctx.Err() != nil {
			return sentinel
		}
		// Never recover from a confirmed bad-credentials wall.
		if isCredentialsRejection(alertText) {
			return fmt.Errorf("%w", ErrCredentialsRejected)
		}
		triedAgent := false
		if a.Recovery != RecoveryOff && a.Agent != nil && !agentUsed && agentEligible(reason) {
			agentUsed = true
			triedAgent = true
			ok, err := a.runAgent(ctx, p, rec, deps, mode, string(reason), &since)
			if err != nil {
				a.log("agent_takeover", map[string]any{"reason": string(reason), "result": "error", "error": err.Error()})
			}
			if ok {
				// The agent reached authentication and confirmed it via the
				// authoritative success gate (cookies/XHR). Trust that instead of
				// re-reading the DOM, which on a slower engine may still show the
				// prior verify/login form mid-navigation.
				agentAuthed = true
				for k := range actedHash {
					delete(actedHash, k)
				}
				return nil
			}
		}
		if a.Recovery != RecoveryOff && a.Handoff != nil && !handoffUsed {
			handoffUsed = true
			if err := a.doHandoff(ctx, reason); err != nil {
				return err
			}
			for k := range actedHash {
				delete(actedHash, k)
			}
			return nil
		}
		if triedAgent {
			return fmt.Errorf("%w", ErrAgentTakeoverFailed)
		}
		return sentinel
	}
	for step := 0; step < maxSteps; step++ {
		_ = p.WaitStable(ctx, browser.DefaultStableTimeout)
		st, hash, err := a.Perceiver.Perceive(ctx, p)
		if err != nil {
			return fmt.Errorf("auth: perceive: %w", err)
		}
		// A successful agent takeover already confirmed authentication; finish now
		// rather than re-detecting the flow on a DOM that may lag the login.
		if agentAuthed {
			a.trace.Add("agent takeover confirmed authentication; finishing")
			a.settleSolve(ctx, true, rec)
			return nil
		}
		// Verification is checked first: a page asking for a code/link is never
		// "done", even if the URL changed (a weak success signal).
		if vs := DetectVerifyStep(st); vs.Needed {
			if a.Verifier == nil {
				return fmt.Errorf("%w: verification required (%s) but no provider configured", ErrFlowFailed, vs.Channel)
			}
			if err := a.doVerify(ctx, p, deps, vs, &since, 0); err != nil {
				// A verification that never arrived is a recovery trigger — the
				// agent can resend or try another channel.
				if errors.Is(err, ErrVerifyTimeout) {
					if rerr := recoverDeadEnd(captcha.ReasonVerifyTimeout, "", fmt.Errorf("%w: verification timed out", ErrStuck)); rerr != nil {
						return rerr
					}
					continue
				}
				return err
			}
			verified = true
			continue
		}
		// Authenticated?
		if ok, ev := (SuccessDetector{}).IsAuthenticated(ctx, p, st, a.base, rec.PostLoginXHRs(), true); ok {
			a.trace.SuccessEvidence = ev
			a.trace.Add("authenticated: %s", strings.Join(ev, ", "))
			a.settleSolve(ctx, true, rec) // reaching authenticated means the CAPTCHA was accepted
			return nil
		}
		// Signup said the account already exists → switch to login.
		if mode == Signup && !verified && IsAccountExists(st) {
			a.trace.Add("signup: account exists → switching to login")
			a.log("flow", map[string]any{"switch": "login", "reason": "account_exists"})
			mode = Login
		}
		f := Classify(st)
		// Is a CAPTCHA present on this form? If so, allow more same-form rounds
		// (the handler re-fills + re-solves each round; the captcha hook escalates
		// to a human when the solver chain is exhausted).
		hasCaptcha := false
		if a.Captcha != nil {
			_, hasCaptcha = captcha.DetectAny(ctx, a.detectors(), p, st, captcha.NetSignals{})
		}
		switch {
		case f.Password != nil:
			limit := 2
			if hasCaptcha {
				limit = 5
			}
			if f.HasAlert && !hasCaptcha && formErrRetries >= 2 {
				// Form-error retries exhausted: a bad-credentials wall fails hard,
				// anything else is a recovery trigger (agent → human).
				if err := recoverDeadEnd(captcha.ReasonStuck, f.AlertText, fmt.Errorf("%w: form error: %s", ErrFlowFailed, f.AlertText)); err != nil {
					return err
				}
				continue
			}
			if actedHash[hash] >= limit {
				if err := recoverDeadEnd(captcha.ReasonStuck, f.AlertText, fmt.Errorf("%w: form did not progress", ErrStuck)); err != nil {
					return err
				}
				continue
			}
			actedHash[hash]++
			// After a signup+verify, or when switched to login, submit login creds;
			// otherwise (fresh signup) use the signup handler.
			h := a.pickHandler(st)
			if mode == Signup && !verified {
				h = SignupHandler{Profile: a.Profile}
			}
			if f.HasAlert {
				formErrRetries++
				a.trace.Add("form error (%s) → recovering, retry %d", f.AlertText, formErrRetries)
				a.log("flow", map[string]any{"recover": f.AlertText})
			} else if verified {
				a.trace.Add("post-verify login")
			}
			if err := h.Run(ctx, st, a.Creds, deps); err != nil {
				// A stranded submit (slow SPA / hydration) is not fatal here: the
				// state hash was already counted, so a repeat reaches the escalation
				// above (agent → human). Any other handler error still aborts.
				if !isStrandedClick(err) {
					return err
				}
				a.trace.Add("submit stranded in resolve (%v)", err)
			}
		case verified || (mode == Signup):
			// Verified a signup but no form here (e.g. "please log in") → go to login.
			if actedHash[hash] >= 1 {
				return fmt.Errorf("%w: no login form after verification", ErrStuck)
			}
			actedHash[hash]++
			if err := a.gotoLogin(ctx, p); err != nil {
				return err
			}
			mode = Login
		case IsSSOOnly(st):
			return fmt.Errorf("%w", ErrSSOOnly)
		default:
			// Unrecognised page with no actionable form. A bot-wall / block page
			// goes straight to a human (the agent must never retry into it);
			// anything else is an agent-eligible unknown flow.
			reason := captcha.ReasonUnknownFlow
			if IsBlockPage(st) {
				reason = captcha.ReasonBlockPage
			}
			if err := recoverDeadEnd(reason, "", fmt.Errorf("%w: unexpected page with no actionable form", ErrUnknownFlow)); err != nil {
				return err
			}
		}
	}
	// Loop exhausted: submit happened but success never fired. Try one recovery,
	// then require a confirmed success before declaring the login done.
	if err := recoverDeadEnd(captcha.ReasonStuck, "", fmt.Errorf("%w: did not authenticate after %d steps", ErrStuck, maxSteps)); err != nil {
		return err
	}
	if ok, ev := a.confirmAuthenticated(ctx, p, rec); ok {
		a.trace.SuccessEvidence = ev
		a.trace.Add("authenticated after recovery: %s", strings.Join(ev, ", "))
		return nil
	}
	return fmt.Errorf("%w: did not authenticate after %d steps", ErrStuck, maxSteps)
}

// captchaHook returns the pre-submit CAPTCHA hook for the flow handlers: it
// detects a challenge, resolves it through the chain (injecting the token), and
// escalates to a human when the chain is exhausted or unsupported.
func (a *Authenticator) captchaHook(p browser.Page) func(context.Context) error {
	if a.Captcha == nil {
		return nil
	}
	return func(ctx context.Context) error {
		st, _, err := a.Perceiver.Perceive(ctx, p)
		if err != nil {
			return nil
		}
		ch, ok := captcha.DetectAny(ctx, a.detectors(), p, st, captcha.NetSignals{})
		if !ok {
			return nil
		}
		// A challenge is present again: any prior solve did not clear it.
		a.settleSolve(ctx, false, nil)
		a.trace.Captcha = append(a.trace.Captcha, string(ch.Vendor))
		a.log("captcha", map[string]any{"vendor": string(ch.Vendor), "sitekey": ch.SiteKey, "kind": string(ch.Kind)})
		// Score / invisible CAPTCHAs (reCAPTCHA v3, invisible v2, Enterprise)
		// are not interactive: the page's own JS runs them on submit and attaches
		// the token. If a solver can supply a token we inject it; otherwise we
		// simply proceed to submit. A score challenge is never a human handoff.
		if ch.Kind == captcha.KindScore {
			if !a.Captcha.Exhausted(ch) {
				if res, err := a.Captcha.Resolve(ctx, p, ch); err == nil {
					a.pendingSolve = res.SolveID
					a.pendingPayload = res.Payload()
				}
			}
			return nil
		}
		// Image/text challenges need the pixels: capture the viewport so the
		// vision solver has something to read.
		if ch.Kind == captcha.KindImage && len(ch.Image) == 0 {
			if img, err := p.Screenshot(ctx); err == nil {
				ch.Image = img
			}
		}
		// Try eligible solvers in order; a decline advances to the next one.
		// A human handoff happens only when the whole chain is exhausted.
		for {
			if a.Captcha.Exhausted(ch) {
				return a.doHandoff(ctx, captcha.ReasonCaptchaExhausted)
			}
			res, err := a.Captcha.Resolve(ctx, p, ch)
			if err == nil {
				a.pendingSolve = res.SolveID
				a.pendingPayload = res.Payload()
				return nil
			}
			if errors.Is(err, captcha.ErrDeclined) || errors.Is(err, captcha.ErrUnsupported) {
				continue // next eligible solver
			}
			if errors.Is(err, captcha.ErrExhausted) || errors.Is(err, captcha.ErrNoSolver) {
				return a.doHandoff(ctx, captcha.ReasonCaptchaExhausted)
			}
			if errors.Is(err, core.ErrBudgetExhausted) {
				return err // surface as failed{budget_exhausted}
			}
			return fmt.Errorf("%w: captcha: %v", ErrFlowFailed, err)
		}
	}
}

func (a *Authenticator) detectors() []captcha.Detector {
	if len(a.Detectors) > 0 {
		return a.Detectors
	}
	return captcha.DefaultDetectors()
}

// reportCaptchaOutcome reports whether a prior solve was accepted, judged by
// whether a challenge is still present.
// settleSolve reports the outcome of the pending CAPTCHA solve (if any) and
// clears it. Acceptance is judged by reliable signals — authenticated ⇒
// accepted; a challenge re-detected before the next submit ⇒ rejected — not by a
// mid-navigation snapshot. Where request bodies are observable (XHR/fetch
// logins) the reached-backend check is logged as corroborating evidence.
func (a *Authenticator) settleSolve(ctx context.Context, accepted bool, rec *handback.Recorder) {
	if a.pendingSolve == "" || a.Captcha == nil {
		return
	}
	if accepted && a.pendingPayload != "" && rec != nil && !rec.TokenSubmitted(a.pendingPayload) {
		a.log("captcha", map[string]any{"accepted": true, "token_observed_in_submit": false})
	}
	_ = a.Captcha.Outcome(ctx, a.pendingSolve, accepted)
	a.pendingSolve = ""
	a.pendingPayload = ""
}

// doHandoff escalates to a human for any dead end.
func (a *Authenticator) doHandoff(ctx context.Context, reason captcha.BlockReason) error {
	if a.Handoff == nil {
		return fmt.Errorf("%w: %s (no human handoff configured)", ErrFlowFailed, reason)
	}
	a.trace.HumanHandoff = string(reason)
	a.log("human_handoff", map[string]any{"reason": string(reason)})
	deadline := time.Now().Add(120 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := a.Handoff.Escalate(ctx, reason, deadline); err != nil {
		if errors.Is(err, captcha.ErrHandoffTimeout) {
			return fmt.Errorf("%w: %v", ErrHandoffTimeout, err)
		}
		return fmt.Errorf("%w: handoff: %v", ErrFlowFailed, err)
	}
	a.trace.Add("human handoff resolved (%s)", reason)
	return nil
}

// sendCodeGrace is how long doVerify polls for an already-delivered (auto-sent)
// code before pressing a "Send code" trigger, so an auto-sending site isn't sent
// a redundant request that burns its re-send cooldown. Correctness does not
// depend on this value — because *since is never advanced, a code that arrives
// after the grace window (even after a needless trigger press) is still matched.
const sendCodeGrace = 8 * time.Second

// doVerify fetches a code/link and applies it to the page.
func (a *Authenticator) doVerify(ctx context.Context, p browser.Page, deps FlowDeps, vs VerifyStep, since *time.Time, verifyFor time.Duration) error {
	vd := verifyFor
	if vd <= 0 {
		vd = a.VerifyTimeout
	}
	if vd <= 0 {
		vd = 90 * time.Second
	}
	deadline := time.Now().Add(vd)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	a.trace.Verification = append(a.trace.Verification, fmt.Sprintf("%s (%s)", vs.Channel, vs.Hint))
	a.log("verification", map[string]any{"channel": string(vs.Channel), "hint": vs.Hint, "link_only": vs.LinkOnly})
	// The verification code reaches us one of two ways: (a) auto-sent when the
	// login form was submitted (e.g. bambulab) — already in flight or in the inbox
	// by the time this screen appears — or (b) only after a "Send code" trigger is
	// pressed. We must NOT assume (b). Pressing "Send code" when the code was
	// already auto-sent burns the server's re-send cooldown, and — the bug this
	// fixes — advancing the freshness window to the click excludes the code already
	// delivered, so the fetch times out on a code sitting in the inbox. So: poll
	// briefly for an already-delivered code first; only if none shows do we press
	// the trigger (once per sign-in), and we NEVER advance *since — GetCode returns
	// the newest unclaimed code, so a superseded one cannot win, and a stale code
	// from before this sign-in is already excluded by the window's start. The
	// trigger is often a React <span/div onClick> with no onclick/role, invisible
	// to the DOM perceiver — hence the JS fallback by text.
	fetch := func(dl time.Time) (verify.Result, error) {
		fctx, fcancel := context.WithDeadline(ctx, dl)
		defer fcancel()
		return a.Verifier.GetCode(fctx, verify.Request{Channel: vs.Channel, Account: a.Account, Target: a.Target, Hint: vs.Hint, WantLink: vs.LinkOnly, Since: *since, Deadline: dl})
	}
	var res verify.Result
	var err error
	if !vs.LinkOnly && !a.codeSent {
		grace := time.Now().Add(sendCodeGrace)
		if grace.After(deadline) {
			grace = deadline
		}
		res, err = fetch(grace) // (a) catch an auto-sent code without touching the trigger
		if err != nil && (errors.Is(err, verify.ErrTimeout) || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() == nil {
			// (b) nothing delivered yet — press the trigger once, then poll the rest.
			clicked := ""
			if vs.SendCode != nil {
				if cerr := (actor{deps}).click(ctx, vs.SendCode, "send-code"); cerr == nil {
					clicked = firstNonEmptyStr(vs.SendCode.Name, vs.SendCode.Text, "send-code")
				}
			}
			if clicked == "" {
				clicked = clickSendCodeJS(ctx, p)
			}
			if clicked != "" {
				a.codeSent = true
				a.trace.Add("verification: triggered code delivery (%q)", strings.TrimSpace(clicked))
				_ = p.WaitStable(ctx, browser.DefaultStableTimeout)
			}
			res, err = fetch(deadline)
		}
	} else {
		res, err = fetch(deadline)
	}
	if err != nil {
		if errors.Is(err, verify.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %v", ErrVerifyTimeout, err)
		}
		return fmt.Errorf("%w: verification: %v", ErrFlowFailed, err)
	}
	// A fetched code is only truly consumed once it has been entered. If entry
	// fails, roll the freshness window back and release the provider's claim so a
	// retry can read the SAME code again (the server may not send another).
	prevSince := *since
	*since = time.Now()
	fetchFailed := func(err error) error {
		*since = prevSince
		if u, ok := a.Verifier.(verify.Unclaimer); ok {
			u.Unclaim(res.MsgID)
		}
		return err
	}
	ac := actor{deps}
	switch res.Kind {
	case verify.ResLink:
		a.trace.Add("verification: opening magic link")
		if err := p.Navigate(ctx, res.Link); err != nil {
			return fetchFailed(fmt.Errorf("auth: open magic link: %w", err))
		}
	case verify.ResCode:
		// Visual entry: a non-perceivable (closed-shadow) code field — focus the
		// coordinate the agent supplied and type the code into focus. The code is
		// typed by the browser, never surfaced to the model.
		if vs.Visual {
			a.trace.Add("verification: entering code (visual)")
			if err := ac.clickAt(ctx, vs.VisualX, vs.VisualY, "otp-focus"); err != nil {
				return fetchFailed(err)
			}
			if err := ac.typeTextFocused(ctx, res.Code, "otp"); err != nil {
				return fetchFailed(err)
			}
			break
		}
		// Re-resolve the code field against FRESH perception right before typing.
		// The field perceived earlier may have re-rendered during the (slow) code
		// fetch, leaving its handle stale; match a fresh node by stable attributes
		// instead of trusting the old handle. Only replace on a confident match, so
		// a non-match keeps the prior behaviour.
		if a.Perceiver != nil {
			if fresh, _, perr := a.Perceiver.Perceive(ctx, p); perr == nil {
				nv := DetectVerifyStep(fresh)
				if vs.CodeField != nil {
					if m, ok := perceive.MatchNode(fresh, *vs.CodeField); ok {
						vs.CodeField = m
					} else if nv.CodeField != nil {
						vs.CodeField = nv.CodeField
					}
				} else {
					vs.CodeField = nv.CodeField
				}
				if vs.Submit != nil {
					if m, ok := perceive.MatchNode(fresh, *vs.Submit); ok {
						vs.Submit = m
					} else if nv.Submit != nil {
						vs.Submit = nv.Submit
					}
				} else if nv.Submit != nil {
					vs.Submit = nv.Submit
				}
			}
		}
		if vs.CodeField == nil {
			return fetchFailed(fmt.Errorf("%w: got a code but found no code field", ErrFlowFailed))
		}
		a.trace.Add("verification: entering code")
		if err := ac.typeInto(ctx, vs.CodeField, res.Code, "otp"); err != nil {
			return fetchFailed(err)
		}
		// Let the code field settle before submitting. A controlled OTP input may
		// validate the code asynchronously (enable Confirm, fire a check), and
		// clicking Confirm within the ~160ms it takes to re-perceive can submit
		// before that completes — observed live as a fresh, correctly-entered code
		// that the form then rejected.
		_ = p.WaitStable(ctx, browser.DefaultStableTimeout)
		f := Classify(mustPerceive(ctx, a, p))
		if err := ac.submit(ctx, Fields{Submit: vs.Submit}, vs.CodeField); err != nil && f.Submit != nil {
			if err2 := ac.click(ctx, f.Submit, "verify"); err2 != nil {
				return fetchFailed(err2)
			}
		}
		// After the submit, record any visible error/status so a REJECTED code (an
		// "incorrect/expired code" message) is distinguishable in the trace from a
		// submit that silently did not fire.
		_ = p.WaitStable(ctx, browser.DefaultStableTimeout)
		a.probeVerifyMessage(ctx, p)
	case verify.ResApproved:
		a.trace.Add("verification: push approved")
	}
	return p.WaitStable(ctx, browser.DefaultStableTimeout)
}

func mustPerceive(ctx context.Context, a *Authenticator, p browser.Page) perceive.State {
	st, _, _ := a.Perceiver.Perceive(ctx, p)
	return st
}

// clickSendCodeJS finds a "Send code" / "Get code" / "Resend code" trigger by its
// visible text and clicks it directly in the page, bypassing the DOM perceiver
// (such triggers are often React <span onClick> elements with no onclick
// attribute/role, so the structured perceiver cannot see them). It only clicks a
// small, visible, leaf-ish element whose text is a send-code phrase, and returns
// the clicked text (or "" if none). Best-effort; never affects the flow on error.
func clickSendCodeJS(ctx context.Context, p browser.Page) string {
	v, err := p.Eval(ctx, `() => {
		const re = /\b(send|get|request|resend)\b[\s\S]{0,16}?\bcode\b/i;
		const els = Array.from(document.querySelectorAll('button, a, [role="button"], span, div, li, p'));
		for (const el of els) {
			if (el.children && el.children.length > 2) continue;
			const t = (el.innerText || el.textContent || '').trim();
			if (!t || t.length > 28 || !re.test(t)) continue;
			const r = el.getBoundingClientRect();
			if (r.width <= 0 || r.height <= 0) continue;
			const cs = getComputedStyle(el);
			if (cs.display === 'none' || cs.visibility === 'hidden') continue;
			el.click();
			return t;
		}
		return '';
	}`)
	if err != nil {
		return ""
	}
	return v.Str()
}

// gotoLogin navigates to the login entrypoint (used when switching from signup).
func (a *Authenticator) gotoLogin(ctx context.Context, p browser.Page) error {
	res := &entry.Resolver{CacheDir: a.EntryCache}
	check := func(st perceive.State, m entry.Mode) (bool, string) { return IsAuthForm(st, Login) }
	_, err := res.Resolve(ctx, a.Target, a.Target, entry.Mode(Login), nil, entry.Deps{
		Page: p, Perceiver: a.Perceiver, Router: a.Router, Check: check, Trace: a.tracef, Timeout: a.Timeout,
	})
	if err != nil {
		return fmt.Errorf("%w: post-signup login entry: %v", ErrFlowFailed, err)
	}
	return nil
}

func (a *Authenticator) pickHandler(st perceive.State) FlowHandler {
	for _, h := range a.Handlers {
		if h.CanHandle(st) {
			return h
		}
	}
	return nil
}

// capture runs the post-login window, builds the bundle, replay-checks it and
// writes the artefacts.
func (a *Authenticator) capture(ctx context.Context, p browser.Page, rec *handback.Recorder, landing string) (handback.Result, error) {
	// Load the landing page once so there is always something to capture.
	if landing == "" {
		landing = p.URL()
	}
	_ = rec.MarkAuthenticated(ctx, a.Capture)
	if landing != "" && landing != p.URL() {
		_ = p.Navigate(ctx, landing)
	}
	// Reload so authenticated XHRs re-fire even when the landing route is a hash
	// change (SPA) that Navigate would not reload. Guarantees the bearer/auth
	// header and /me-style calls land in the post-login window.
	_ = p.Reload(ctx)
	rec.WaitWindow(ctx)

	state, err := p.ExportState(ctx)
	if err != nil {
		return handback.Result{}, fmt.Errorf("auth: export state: %w", err)
	}
	recording, err := rec.Finalize(ctx)
	if err != nil {
		return handback.Result{}, fmt.Errorf("auth: finalize recording: %w", err)
	}
	authHeaders := rec.AuthHeaders()

	bundle := handback.SessionBundle{
		Target: a.Target, Account: a.Account, Origin: state.Origin,
		Cookies: state.Cookies, LocalStorage: state.LocalStorage, SessionStorage: state.SessionStorage,
		AuthHeaders: authHeaders, UserAgent: state.UserAgent, LandingURL: landing, CapturedAt: time.Now().UTC(),
		Backend: a.backend.Name, BackendVersion: a.backend.Version,
	}
	// Token expiry from bearer/JWT in headers or storage.
	vals := []string{authHeaders["authorization"]}
	for _, v := range state.LocalStorage {
		vals = append(vals, v)
	}
	bundle.TokenExpiry = JWTExpiry(vals...)

	replayer := handback.NewHTTPReplayChecker()
	replayable, reason, _ := replayer.Check(ctx, bundle, recording, rec.VaryingHeaders())
	bundle.Replayable = replayable
	bundle.ReplayReason = reason
	a.trace.Replayable = replayable
	a.trace.ReplayReason = reason
	a.trace.Add("replayability: %v %s", replayable, reason)

	// Persist the saved session for restore-without-login.
	if a.Sessions != nil {
		_ = a.Sessions.Save(ctx, SavedSession{Target: a.Target, LandingURL: landing, State: state, AuthHeaders: authHeaders, SavedAt: time.Now().UTC()})
	}

	bref, err := a.Writer.WriteBundle(ctx, bundle)
	if err != nil {
		return handback.Result{}, fmt.Errorf("auth: write bundle: %w", err)
	}
	rref, err := a.Writer.WriteRecording(ctx, recording)
	if err != nil {
		return handback.Result{}, fmt.Errorf("auth: write recording: %w", err)
	}
	a.trace.FinishedAt = time.Now().UTC()
	a.trace.Timings["total_ms"] = fmt.Sprintf("%d", a.trace.FinishedAt.Sub(a.trace.StartedAt).Milliseconds())
	a.log("handback", map[string]any{"bundle": bref.URI, "recording": rref.URI, "replayable": replayable})
	return handback.Result{BundleRef: bref, RecordingRef: rref, Trace: a.trace}, nil
}

func (a *Authenticator) finish(ctx context.Context, p browser.Page, rec *handback.Recorder) (handback.Result, error) {
	landing := p.URL()
	return a.capture(ctx, p, rec, landing)
}

func (a *Authenticator) log(kind string, payload map[string]any) {
	if a.Audit == nil {
		return
	}
	_, _ = a.Audit.Append(context.Background(), audit.Entry{Actor: "authenticator", Kind: kind, Payload: payload})
}

func (a *Authenticator) tracef(format string, args ...any) { a.trace.Add(format, args...) }

// FailReason maps a Run error to a stable job failure reason.
func FailReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrSSOOnly):
		return "sso_only"
	case errors.Is(err, ErrEntryNotFound):
		return "entrypoint_not_found"
	case errors.Is(err, ErrUnknownFlow):
		return "unknown_flow"
	case errors.Is(err, ErrNotAuthenticated):
		return "not_authenticated"
	case errors.Is(err, ErrStuck):
		return "stuck"
	case errors.Is(err, ErrCredentialsRejected):
		return "credentials_rejected"
	case errors.Is(err, ErrAgentTakeoverFailed):
		return "agent_takeover_failed"
	case errors.Is(err, ErrVerifyTimeout):
		return "verify_timeout"
	case errors.Is(err, ErrHandoffTimeout):
		return "handoff_timeout"
	case errors.Is(err, core.ErrBudgetExhausted):
		return "budget_exhausted"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	default:
		return "flow_failed"
	}
}
