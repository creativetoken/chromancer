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

	trace          handback.Trace
	base           Baseline
	pendingSolve   string
	pendingPayload string // token/text of the pending solve (for the reached-backend check)
}

// Run authenticates (target, account) in the given mode and returns the
// handback result. It restores a healthy saved session without logging in,
// else runs the login/signup flow, handles verification/CAPTCHA via the
// provided hooks, detects success, captures the post-login window, builds the
// SessionBundle, runs the replayability check and writes the artefacts.
func (a *Authenticator) Run(ctx context.Context, b *browser.Browser, mode Mode, hints Hints) (handback.Result, error) {
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
		return handback.Result{}, err
	}

	// 6. Success → post-login capture → bundle → replay → write.
	return a.finish(ctx, p, rec)
}

func (a *Authenticator) tryRestore(ctx context.Context, p *browser.Page, rec *handback.Recorder) (bool, handback.Result, bool) {
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
	ok, ev := SuccessDetector{}.IsAuthenticated(ctx, p, st, base, rec.PostLoginXHRs())
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

func (a *Authenticator) authenticate(ctx context.Context, p *browser.Page, rec *handback.Recorder, mode Mode, hints Hints) error {
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
		return err
	}
	return a.resolve(ctx, p, rec, deps, mode)
}

// resolve is the post-submit loop: success | verification | account-exists →
// login | form-error recovery | SSO-only | stuck.
func (a *Authenticator) resolve(ctx context.Context, p *browser.Page, rec *handback.Recorder, deps FlowDeps, mode Mode) error {
	maxSteps := a.MaxSteps
	if maxSteps == 0 {
		maxSteps = 6
	}
	formErrRetries := 0
	verified := false
	handoffUsed := false
	var since time.Time
	actedHash := map[perceive.StateHash]int{}
	// escalate hands a dead end (stuck / unknown flow) to a human when one is
	// configured, once; otherwise it returns the sentinel error.
	escalate := func(reason captcha.BlockReason, sentinel error) error {
		if a.Handoff != nil && !handoffUsed {
			handoffUsed = true
			if err := a.doHandoff(ctx, reason); err != nil {
				return err
			}
			// The human intervened; give the form a fresh chance to progress.
			for k := range actedHash {
				delete(actedHash, k)
			}
			return nil
		}
		return sentinel
	}
	for step := 0; step < maxSteps; step++ {
		_ = p.WaitStable(ctx, browser.DefaultStableTimeout)
		st, hash, err := a.Perceiver.Perceive(ctx, p)
		if err != nil {
			return fmt.Errorf("auth: perceive: %w", err)
		}
		// Report the outcome of any prior CAPTCHA solve (accepted iff the
		// challenge is gone AND the token actually reached the backend).
		a.reportCaptchaOutcome(ctx, p, st, rec)
		// Verification is checked first: a page asking for a code/link is never
		// "done", even if the URL changed (a weak success signal).
		if vs := DetectVerifyStep(st); vs.Needed {
			if a.Verifier == nil {
				return fmt.Errorf("%w: verification required (%s) but no provider configured", ErrFlowFailed, vs.Channel)
			}
			if err := a.doVerify(ctx, p, deps, vs, &since); err != nil {
				return err
			}
			verified = true
			continue
		}
		// Authenticated?
		if ok, ev := (SuccessDetector{}).IsAuthenticated(ctx, p, st, a.base, rec.PostLoginXHRs()); ok {
			a.trace.SuccessEvidence = ev
			a.trace.Add("authenticated: %s", strings.Join(ev, ", "))
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
				return fmt.Errorf("%w: form error: %s", ErrFlowFailed, f.AlertText)
			}
			if actedHash[hash] >= limit {
				if err := escalate(captcha.ReasonStuck, fmt.Errorf("%w: form did not progress", ErrStuck)); err != nil {
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
				return err
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
			// Unrecognised page with no actionable form → escalate to a human.
			if err := escalate(captcha.ReasonUnknownFlow, fmt.Errorf("%w: unexpected page with no actionable form", ErrUnknownFlow)); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("%w: did not authenticate after %d steps", ErrStuck, maxSteps)
}

// captchaHook returns the pre-submit CAPTCHA hook for the flow handlers: it
// detects a challenge, resolves it through the chain (injecting the token), and
// escalates to a human when the chain is exhausted or unsupported.
func (a *Authenticator) captchaHook(p *browser.Page) func(context.Context) error {
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
func (a *Authenticator) reportCaptchaOutcome(ctx context.Context, p *browser.Page, st perceive.State, rec *handback.Recorder) {
	if a.pendingSolve == "" || a.Captcha == nil {
		return
	}
	_, still := captcha.DetectAny(ctx, a.detectors(), p, st, captcha.NetSignals{})
	// A token/text solve counts as applied only if it actually reached the
	// backend (observed in an outgoing request). Score/invisible solves carry no
	// payload we inject, so they are always "applied".
	applied := a.pendingPayload == "" || (rec != nil && rec.TokenSubmitted(a.pendingPayload))
	accepted := applied && !still
	if a.pendingPayload != "" && !applied {
		a.log("captcha", map[string]any{"applied": false, "note": "solved token not observed in submit"})
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

// doVerify fetches a code/link and applies it to the page.
func (a *Authenticator) doVerify(ctx context.Context, p *browser.Page, deps FlowDeps, vs VerifyStep, since *time.Time) error {
	deadline := time.Now().Add(90 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	a.trace.Verification = append(a.trace.Verification, fmt.Sprintf("%s (%s)", vs.Channel, vs.Hint))
	a.log("verification", map[string]any{"channel": string(vs.Channel), "hint": vs.Hint, "link_only": vs.LinkOnly})
	vctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	res, err := a.Verifier.GetCode(vctx, verify.Request{Channel: vs.Channel, Account: a.Account, Hint: vs.Hint, Since: *since, Deadline: deadline})
	if err != nil {
		return fmt.Errorf("%w: verification: %v", ErrFlowFailed, err)
	}
	*since = time.Now()
	ac := actor{deps}
	switch res.Kind {
	case verify.ResLink:
		a.trace.Add("verification: opening magic link")
		if err := p.Navigate(ctx, res.Link); err != nil {
			return fmt.Errorf("auth: open magic link: %w", err)
		}
	case verify.ResCode:
		if vs.CodeField == nil {
			// Re-perceive to locate the code field.
			st, _, _ := a.Perceiver.Perceive(ctx, p)
			nv := DetectVerifyStep(st)
			vs.CodeField, vs.Submit = nv.CodeField, nv.Submit
		}
		if vs.CodeField == nil {
			return fmt.Errorf("%w: got a code but found no code field", ErrFlowFailed)
		}
		a.trace.Add("verification: entering code")
		if err := ac.typeInto(ctx, vs.CodeField, res.Code, "otp"); err != nil {
			return err
		}
		f := Classify(mustPerceive(ctx, a, p))
		if err := ac.submit(ctx, Fields{Submit: vs.Submit}, vs.CodeField); err != nil && f.Submit != nil {
			if err2 := ac.click(ctx, f.Submit, "verify"); err2 != nil {
				return err2
			}
		}
	case verify.ResApproved:
		a.trace.Add("verification: push approved")
	}
	return p.WaitStable(ctx, browser.DefaultStableTimeout)
}

func mustPerceive(ctx context.Context, a *Authenticator, p *browser.Page) perceive.State {
	st, _, _ := a.Perceiver.Perceive(ctx, p)
	return st
}

// gotoLogin navigates to the login entrypoint (used when switching from signup).
func (a *Authenticator) gotoLogin(ctx context.Context, p *browser.Page) error {
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
func (a *Authenticator) capture(ctx context.Context, p *browser.Page, rec *handback.Recorder, landing string) (handback.Result, error) {
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

func (a *Authenticator) finish(ctx context.Context, p *browser.Page, rec *handback.Recorder) (handback.Result, error) {
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
