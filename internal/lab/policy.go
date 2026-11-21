package lab

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/creativetoken/chromancer/internal/model"
)

// PolicyModel is a TEST-ONLY, deterministic LLMProvider that reads the agent's
// OBSERVATION text and returns one action per step. It is the "lab policy":
// a fixed, observation-driven decision function that drives the whole lab auth
// surface (and a basic crawl) without any real model, so the agent-only auth and
// crawl paths can be exercised end to end in tests on both browser backends.
//
// It is NOT a general model and must never be used outside tests: NewPolicyModel
// panics when it is constructed outside `go test` (testing.Testing()).
//
// The policy never sees real secrets — it fills credentials through the
// fill_login_form tool (or types the {{account.*}} placeholders the Executor
// substitutes), exactly as a real agent would — so the "no secret reaches the
// model" assertions hold with the lab policy just as they do with a real
// provider.
type PolicyModel struct {
	name string

	// Minimal run-local memory for loop-breaking. One PolicyModel instance drives
	// one run, so per-instance counters are safe. Everything else is derived fresh
	// from each observation.
	dismissed     bool
	resent        bool
	clicked       map[int]bool
	checks        int
	captchaSolves int
	calls         int
}

// NewPolicyModel returns the lab policy. It panics outside tests.
func NewPolicyModel() *PolicyModel {
	if !testing.Testing() {
		panic("lab.PolicyModel is test-only and must not be used outside tests")
	}
	return &PolicyModel{name: "lab-policy", clicked: map[int]bool{}}
}

// Name implements model.LLMProvider.
func (p *PolicyModel) Name() string { return p.name }

// Capabilities implements model.LLMProvider.
func (p *PolicyModel) Capabilities() model.Capabilities {
	return model.Capabilities{NativeJSONSchema: true, JSONMode: true, MaxContextTokens: 32000, MaxOutputTokens: 1024}
}

// Calls returns how many decisions the policy has made (test accessor).
func (p *PolicyModel) Calls() int { return p.calls }

// Chat implements model.LLMProvider: it returns the chosen action as JSON text.
func (p *PolicyModel) Chat(ctx context.Context, msgs []model.Message, _ model.ChatOpts) (*model.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw := p.act(lastUser(msgs), nil)
	return &model.Completion{Text: string(raw), Usage: model.Usage{InputTokens: 20, OutputTokens: 8}}, nil
}

// Structured implements model.LLMProvider: one schema-valid action object.
func (p *PolicyModel) Structured(ctx context.Context, msgs []model.Message, schema json.RawMessage, _ model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	raw := p.act(lastUser(msgs), schema)
	return raw, &model.Usage{InputTokens: 20, OutputTokens: 8}, nil
}

func lastUser(msgs []model.Message) string {
	obs := ""
	for _, m := range msgs {
		if m.Role == model.User {
			obs = m.Text
		}
	}
	return obs
}

// act is the decision function: parse the observation, choose one action.
func (p *PolicyModel) act(obs string, schema json.RawMessage) json.RawMessage {
	p.calls++
	avail := actionsFromSchema(schema)
	o := parseObservation(obs)
	a := p.decide(o, avail)
	raw, _ := json.Marshal(a)
	return raw
}

// --- decision logic --------------------------------------------------------

type act map[string]any

func (p *PolicyModel) decide(o observation, avail map[string]bool) act {
	has := func(name string) bool { return avail == nil || avail[name] }

	// 1. Bot-wall / block page → straight to a human (never click through).
	if has("request_human") && o.botWall && !o.hasCaptchaWidget {
		return act{"action": "request_human", "why": "bot-wall / block page", "reason": "the site is showing a bot-wall; a human must take over"}
	}

	// 2. Already authenticated (a logout/sign-out control is present) → confirm.
	if has("check_logged_in") && o.loggedInHint {
		return act{"action": "check_logged_in", "why": "a logout control is present; confirm the session"}
	}

	// 2b. A hard credentials rejection after we have already submitted → give up.
	// Do NOT retry wrong credentials (a transient "try again" is not a rejection
	// and is handled by the normal re-fill path below).
	if o.credsRejected && p.calls > 1 {
		if has("give_up") {
			return act{"action": "give_up", "why": "credentials rejected", "reason": "the site rejected the credentials"}
		}
	}

	// 3. CAPTCHA widget → solve via the tool, but fill a login form first (the
	// solve injects a token the form then submits). Solve once per challenge; a
	// captcha error re-arms a solve.
	if has("solve_captcha") && o.hasCaptchaWidget {
		needFillFirst := o.passwordField != nil && !(o.loginFilled || o.passwordField.filled)
		if o.captchaError {
			p.captchaSolves = 0
		}
		if !needFillFirst && p.captchaSolves < 1 {
			p.captchaSolves++
			return act{"action": "solve_captcha", "why": "solve the CAPTCHA before submitting"}
		}
	}

	// 4. Cookie/consent banner → dismiss once (privacy-preserving).
	if has("dismiss_banner") && o.consentBanner && !p.dismissed {
		p.dismissed = true
		return act{"action": "dismiss_banner", "why": "dismiss the cookie/consent banner"}
	}

	// 5. Terms/consent checkbox → tick it before continuing.
	if cb := o.agreeCheckbox; cb != nil && !p.clicked[cb.id] {
		p.clicked[cb.id] = true
		return act{"action": "click", "id": cb.id, "why": "accept the terms"}
	}

	// 6. Verification step (a code field, or the page asks for a code/link).
	if (o.codeField != nil || o.asksForCode) && has("enter_verification") {
		// Request a fresh code when (a) the page says none was delivered yet, or
		// (b) a prior enter_verification just failed/timed out — and a resend
		// control is present and we have not already resent.
		if o.resendCtl != nil && has("resend_verification") && !p.resent &&
			(o.needsResendFirst || o.verifyFailed) {
			p.resent = true
			return act{"action": "resend_verification", "why": "no code arrived; request a fresh one"}
		}
		want := "code"
		if o.wantLink {
			want = "link"
		}
		a := act{"action": "enter_verification", "channel": o.channel, "want": want, "why": "fetch and enter the verification " + want}
		if o.codeField != nil {
			a["id"] = o.codeField.id
		}
		return a
	}

	// 6b. Signup form (a second/confirm password, or a register-style submit) →
	// fill_signup_form, which also submits.
	if o.signupForm && has("fill_signup_form") {
		p.checks = 0
		return act{"action": "fill_signup_form", "why": "fill the registration form"}
	}

	// 7. Login form present.
	if pw := o.passwordField; pw != nil {
		if o.loginFilled || pw.filled {
			if btn := o.submitButton; btn != nil {
				return act{"action": "click", "id": btn.id, "why": "submit the login form"}
			}
		}
		if has("fill_login_form") {
			return act{"action": "fill_login_form", "why": "fill the username and password"}
		}
		// No fill tool (crawl mode): type the placeholders the Executor substitutes.
		if o.identifierField != nil && !o.identifierField.filled {
			return act{"action": "type", "id": o.identifierField.id, "text": "{{account.email}}", "why": "enter the email"}
		}
		return act{"action": "type", "id": pw.id, "text": "{{account.password}}", "why": "enter the password"}
	}

	// 8. Identifier-only step (multi-step: email first, then password) → type the
	// email placeholder and advance.
	if id := o.identifierField; id != nil && !id.filled {
		return act{"action": "type", "id": id.id, "text": "{{account.email}}", "why": "enter the email to continue"}
	}
	if o.identifierField != nil && o.identifierField.filled {
		if btn := o.submitButton; btn != nil {
			return act{"action": "click", "id": btn.id, "why": "continue to the next step"}
		}
	}

	// 9. Interstitial / modal: a primary continue-style button, no form.
	if btn := o.primaryButton; btn != nil {
		return act{"action": "click", "id": btn.id, "why": "continue through the interstitial"}
	}

	// 9b. No form on this page but a "Sign in" / "Members area" control that opens
	// one (a modal trigger or an entry link) → click it to reveal the form.
	if o.passwordField == nil && o.codeField == nil && o.identifierField == nil {
		if op := o.loginOpener; op != nil && !p.clicked[op.id] {
			p.clicked[op.id] = true
			return act{"action": "click", "id": op.id, "why": "open the sign-in form"}
		}
	}

	// 10. Nothing to fill and we have submitted something → confirm the session.
	if has("check_logged_in") {
		p.checks++
		if p.checks <= 4 {
			return act{"action": "check_logged_in", "why": "no form to act on; check whether login succeeded"}
		}
		if has("request_human") {
			return act{"action": "request_human", "why": "no actionable path", "reason": "the agent could not find a way forward"}
		}
		return act{"action": "give_up", "why": "no actionable path", "reason": "no form, no verification, not authenticated"}
	}

	// Crawl mode fallback: click the first unseen link/button, else stop.
	if btn := o.firstNavigable; btn != nil {
		return act{"action": "click", "id": btn.id, "why": "explore this control"}
	}
	return act{"action": "done", "why": "no further actions", "reason": "nothing left to explore"}
}

// --- observation parsing ---------------------------------------------------

type element struct {
	id     int
	kind   string // tag or tag/role
	name   string
	attrs  string
	typ    string
	filled bool // a non-empty value (password shows as ***)
}

type observation struct {
	url, title, message string
	data                string
	elems               []element
	recent              []string // "action → outcome" lines, oldest→newest

	botWall          bool
	hasCaptchaWidget bool
	captchaError     bool
	consentBanner    bool
	loggedInHint     bool
	asksForCode      bool
	credsRejected    bool
	signupForm       bool
	wantLink         bool
	channel          string
	needsResendFirst bool
	loginFilled      bool
	verifyFailed     bool

	identifierField  *element
	passwordField    *element
	codeField        *element
	submitButton     *element
	submitCandidates []*element
	primaryButton    *element
	agreeCheckbox    *element
	resendCtl        *element
	loginOpener      *element
	firstNavigable   *element
}

var (
	elemRe   = regexp.MustCompile(`(?m)^#(\d+)\s+(\S+)\s+"([^"]*)"(?:\s+\((.*)\))?\s*$`)
	recentRe = regexp.MustCompile(`(?m)^\s+(\w+)\s+→\s+(.*)$`)

	botWallRe   = regexp.MustCompile(`(?i)(verify (you(’|'|)re|you are) (a )?human|are you a (human|robot)|unusual traffic|automated (traffic|requests)|access denied|request blocked|you have been blocked|rate.?limit|too many requests|press (and hold|& hold)|complete the (security )?check|cloudflare)`)
	captchaRe   = regexp.MustCompile(`(?i)(recaptcha|hcaptcha|turnstile|i'?m not a robot|captcha|select all (images|squares))`)
	consentRe   = regexp.MustCompile(`(?i)(cookie|consent|we use cookies|privacy preferences)`)
	acceptRe    = regexp.MustCompile(`(?i)(accept|agree|got it|allow all|reject|decline|only necessary|necessary only)`)
	logoutRe    = regexp.MustCompile(`(?i)(log ?out|sign ?out)`)
	codeAskRe   = regexp.MustCompile(`(?i)(verification code|enter the code|one-?time (code|password)|security code|we (emailed|sent|texted)|check your (email|phone|inbox)|authenticator|6-digit)`)
	linkAskRe   = regexp.MustCompile(`(?i)(magic link|sign-?in link|click the link|we emailed you a link|open the link)`)
	smsRe       = regexp.MustCompile(`(?i)(sms|text message|your phone|phone number|texted)`)
	totpRe      = regexp.MustCompile(`(?i)(authenticator|authentication app|authentication code|totp|google authenticator|authy)`)
	codeFieldRe = regexp.MustCompile(`(?i)(code|otp|one-?time|verification|passcode|2fa|mfa)`)
	resendRe    = regexp.MustCompile(`(?i)(resend|send (a |another )?(new )?code|didn'?t (get|receive))`)
	noCodeYetRe = regexp.MustCompile(`(?i)(resend|didn'?t (get|receive)|haven'?t received|request a (new )?code)`)
	submitRe    = regexp.MustCompile(`(?i)^(log ?in|sign ?in|submit|continue|next|verify|confirm)$`)
	primaryRe   = regexp.MustCompile(`(?i)^(continue.*|proceed.*|confirm.*|trust this device|got it|next|verify|yes.*|allow.*|i agree.*|accept.*)$`)
	avoidRe     = regexp.MustCompile(`(?i)(delete|remove|close account|deactivate|cancel|log ?out|sign ?out)`)
	openerRe    = regexp.MustCompile(`(?i)^(sign ?in|log ?in|login|enter|members?( area| portal)?|get started|my account|account)$`)
	registerRe  = regexp.MustCompile(`(?i)(create account|create an account|sign ?up|register|join( now)?)`)
	// credRejectRe matches a hard credentials rejection (wrong password / unknown
	// account), as opposed to a transient "something went wrong, try again".
	credRejectRe = regexp.MustCompile(`(?i)(incorrect|invalid|wrong|not ?recogni[sz]ed|bad)[^.]{0,24}(password|e-?mail|username|credential|login|account)|invalid credentials|(password|e-?mail|username)[^.]{0,24}(incorrect|invalid|wrong)`)
)

func parseObservation(obs string) observation {
	o := observation{channel: "email"}
	o.url = section1(obs, "URL:")
	o.title = section1(obs, "Title:")
	o.message = section1(obs, "Message:")
	if i := strings.Index(obs, "DATA (page text"); i >= 0 {
		o.data = obs[i:]
	}

	for _, m := range elemRe.FindAllStringSubmatch(obs, -1) {
		id, _ := strconv.Atoi(m[1])
		e := element{id: id, kind: m[2], name: m[3], attrs: m[4]}
		e.typ = attrValue(e.attrs, "type")
		e.filled = strings.Contains(e.attrs, "value=")
		o.elems = append(o.elems, e)
	}
	for _, m := range recentRe.FindAllStringSubmatch(obs, -1) {
		o.recent = append(o.recent, m[1]+" → "+m[2])
	}

	hay := o.title + " " + o.message + " " + o.data
	o.botWall = botWallRe.MatchString(hay)
	o.consentBanner = consentRe.MatchString(hay) && hasConsentButton(o.elems)
	o.asksForCode = codeAskRe.MatchString(hay)
	// A credentials rejection is judged from the alert/title ONLY — never the full
	// page DATA, where the alert text runs into form labels ("…wrong, please try
	// again Email Password…") and yields false positives.
	o.credsRejected = credRejectRe.MatchString(o.title + " " + o.message)
	o.captchaError = regexp.MustCompile(`(?i)captcha`).MatchString(o.message) &&
		regexp.MustCompile(`(?i)(incorrect|failed|invalid|try again|complete)`).MatchString(o.message)
	o.wantLink = linkAskRe.MatchString(hay)
	o.needsResendFirst = noCodeYetRe.MatchString(hay) && !codeAlreadySent(hay)
	// SMS/phone cues win over generic 2FA wording (an SMS page may also say
	// "two-step verification"); then an explicit authenticator cue; else email.
	switch {
	case smsRe.MatchString(hay):
		o.channel = "sms"
	case totpRe.MatchString(hay):
		o.channel = "totp"
	default:
		o.channel = "email"
	}

	// Did the agent just fill the login form (so the next move is submit)?
	if n := len(o.recent); n > 0 {
		last := o.recent[n-1]
		o.loginFilled = strings.HasPrefix(last, "fill_login_form") && !strings.Contains(last, "error")
	}
	for _, r := range o.recent {
		if strings.HasPrefix(r, "enter_verification") && (strings.Contains(r, "error") || strings.Contains(r, "timed out") || strings.Contains(r, "timeout")) {
			o.verifyFailed = true
		}
	}

	// Classify elements.
	for i := range o.elems {
		e := &o.elems[i]
		low := strings.ToLower(e.name + " " + e.attrs)
		switch {
		case e.typ == "password":
			if o.passwordField == nil {
				o.passwordField = e
			}
		case e.typ == "checkbox" && (strings.Contains(low, "agree") || strings.Contains(low, "terms") || strings.Contains(low, "consent")):
			if o.agreeCheckbox == nil {
				o.agreeCheckbox = e
			}
		case isTextInput(e) && codeFieldRe.MatchString(low):
			if o.codeField == nil {
				o.codeField = e
			}
		case isTextInput(e) && (e.typ == "email" || strings.Contains(low, "email") || strings.Contains(low, "user") || strings.Contains(low, "phone")):
			if o.identifierField == nil {
				o.identifierField = e
			}
		}
		if isButton(e) {
			name := strings.TrimSpace(e.name)
			if o.resendCtl == nil && resendRe.MatchString(name) {
				o.resendCtl = e
			}
			if !avoidRe.MatchString(name) && !resendRe.MatchString(name) {
				o.submitCandidates = append(o.submitCandidates, e)
			}
			if o.primaryButton == nil && primaryRe.MatchString(name) && !avoidRe.MatchString(name) {
				o.primaryButton = e
			}
			if captchaRe.MatchString(name) {
				o.hasCaptchaWidget = true
			}
		}
		if o.loginOpener == nil && (isButton(e) || e.kind == "a") && openerRe.MatchString(strings.TrimSpace(e.name)) && !avoidRe.MatchString(e.name) {
			o.loginOpener = e
		}
		if o.firstNavigable == nil && (isButton(e) || e.kind == "a") && !avoidRe.MatchString(e.name) {
			o.firstNavigable = e
		}
		if logoutRe.MatchString(e.name) {
			o.loggedInHint = true
		}
	}
	if captchaRe.MatchString(hay) && !o.botWall {
		o.hasCaptchaWidget = true
	}
	// A registration form: a second/confirm password field, or a register-style
	// submit button, alongside a password field. Distinguishes signup from login
	// so the agent fills (and submits) via fill_signup_form.
	pwCount := 0
	for i := range o.elems {
		if o.elems[i].typ == "password" {
			pwCount++
		}
	}
	registerBtn := false
	for i := range o.elems {
		if isButton(&o.elems[i]) && registerRe.MatchString(o.elems[i].name) {
			registerBtn = true
		}
	}
	o.signupForm = o.passwordField != nil && (pwCount >= 2 || registerBtn)
	// Choose the form's submit control from the candidate buttons, preferring a
	// real type=submit over a same-named type=button (e.g. a modal's header "Sign
	// in" opener vs the form's submit), then a submit-worded button, then any.
	o.submitButton = pickSubmit(o.submitCandidates)
	if o.submitButton == nil {
		o.submitButton = o.primaryButton
	}
	return o
}

// pickSubmit chooses the best submit control: a real type=submit button, else a
// submit-worded button, else the first candidate. type=button controls (JS
// toggles like a modal opener) are only used as a last resort.
func pickSubmit(cands []*element) *element {
	var submitTyped, named, plain *element
	for _, e := range cands {
		switch {
		case e.typ == "submit" && submitTyped == nil:
			submitTyped = e
		case submitRe.MatchString(strings.TrimSpace(e.name)) && e.typ != "button" && named == nil:
			named = e
		case e.typ != "button" && plain == nil:
			plain = e
		}
	}
	switch {
	case submitTyped != nil:
		return submitTyped
	case named != nil:
		return named
	case plain != nil:
		return plain
	case len(cands) > 0:
		return cands[0]
	}
	return nil
}

func isTextInput(e *element) bool {
	if e.kind != "input" && e.kind != "textarea" {
		return false
	}
	switch e.typ {
	case "", "text", "email", "tel", "number":
		return true
	}
	return false
}

func isButton(e *element) bool {
	return e.kind == "button" || strings.HasSuffix(e.kind, "/button") || e.typ == "submit"
}

func hasConsentButton(elems []element) bool {
	for i := range elems {
		if isButton(&elems[i]) && acceptRe.MatchString(elems[i].name) {
			return true
		}
	}
	return false
}

func codeAlreadySent(hay string) bool {
	return regexp.MustCompile(`(?i)(a new code has been sent|we('| have| ve)? sent|code (has been )?sent|just sent)`).MatchString(hay)
}

func section1(obs, label string) string {
	for _, line := range strings.Split(obs, "\n") {
		if strings.HasPrefix(line, label) {
			return strings.TrimSpace(strings.TrimPrefix(line, label))
		}
	}
	return ""
}

func attrValue(attrs, key string) string {
	for _, part := range strings.Split(attrs, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, key+"=") {
			return strings.TrimPrefix(part, key+"=")
		}
	}
	return ""
}

// actionsFromSchema extracts the allowed action enum from the action schema so
// the policy never emits a tool that is not wired. A nil schema means "anything".
func actionsFromSchema(schema json.RawMessage) map[string]bool {
	if len(schema) == 0 {
		return nil
	}
	var s struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil || len(s.Properties.Action.Enum) == 0 {
		return nil
	}
	m := map[string]bool{}
	for _, a := range s.Properties.Action.Enum {
		m[a] = true
	}
	return m
}
