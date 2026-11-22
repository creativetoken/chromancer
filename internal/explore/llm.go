package explore

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/model"
	"github.com/creativetoken/chromancer/internal/perceive"
)

//go:embed prompts/crawl_system.md
var defaultSystemPrompt string

// LLMModel is the narrow slice of model.LLMProvider the crawl needs: one
// schema-constrained decision per step. model.LLMProvider satisfies it; tests
// supply a scripted mock.
type LLMModel interface {
	Structured(ctx context.Context, msgs []model.Message, schema json.RawMessage, opts model.ChatOpts) (json.RawMessage, *model.Usage, error)
}

// llmAction is one decision from the model.
type llmAction struct {
	Action  string `json:"action"`
	ID      int    `json:"id"`
	Text    string `json:"text"`
	Option  string `json:"option"`
	URL     string `json:"url"`
	Dir     string `json:"dir"`
	Ms      int    `json:"ms"`
	Why     string `json:"why"`
	Reason  string `json:"reason"`
	Channel string `json:"channel"` // enter_verification: email|sms|totp
	Want    string `json:"want"`    // enter_verification: code|link
	X       int    `json:"x"`       // click_at: viewport x (CSS px)
	Y       int    `json:"y"`       // click_at: viewport y (CSS px)
}

// actionSchema constrains the model to one action object. This is the base
// (crawl) action set; takeover mode extends the enum via buildSchema.
var actionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["click","type","select","submit","navigate","scroll","back","wait","note","done"]},
    "id": {"type": "integer", "description": "numeric id of the target element from the OBSERVATION"},
    "text": {"type": "string", "description": "text to type (type action)"},
    "option": {"type": "string", "description": "option to choose (select action)"},
    "url": {"type": "string", "description": "URL to open (navigate action)"},
    "dir": {"type": "string", "enum": ["up","down"], "description": "scroll direction"},
    "ms": {"type": "integer", "description": "milliseconds to wait (wait action)"},
    "why": {"type": "string", "description": "one line reason for this action"},
    "reason": {"type": "string", "description": "why you are stopping (done action)"}
  },
  "required": ["action", "why"],
  "additionalProperties": false
}`)

// buildSchema returns the action schema for this driver. In takeover mode it
// extends the base enum with the verification/captcha/success/human actions
// whose callbacks are wired (only those), so the model is never offered a tool
// that is not actually available.
func (e *Explorer) buildSchema() json.RawMessage {
	if !e.takeoverMode() {
		return actionSchema
	}
	actions := []string{"click", "type", "select", "submit", "navigate", "scroll", "back", "wait", "note", "done"}
	if e.EnterVerification != nil {
		actions = append(actions, "enter_verification")
	}
	if e.ResendVerification != nil {
		actions = append(actions, "resend_verification")
	}
	if e.SolveCaptcha != nil {
		actions = append(actions, "solve_captcha")
	}
	if e.CheckLoggedIn != nil {
		actions = append(actions, "check_logged_in")
	}
	if e.RequestHuman != nil {
		actions = append(actions, "request_human")
	}
	if e.FillLoginForm != nil {
		actions = append(actions, "fill_login_form")
	}
	if e.FillSignupForm != nil {
		actions = append(actions, "fill_signup_form")
	}
	if e.DismissBanner != nil {
		actions = append(actions, "dismiss_banner")
	}
	if e.visionAvailable() {
		// Visual-mode actions. look takes a fresh set-of-marks screenshot;
		// click(id) acts on a marked node by its centre; click_at(x,y) clicks a
		// region with no marked node; type_text types into the focused field.
		actions = append(actions, "look", "click_at", "type_text")
	}
	actions = append(actions, "give_up")
	enumJSON, _ := json.Marshal(actions)
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": %s},
    "id": {"type": "integer", "description": "numeric id of the target element from the OBSERVATION"},
    "text": {"type": "string", "description": "text to type (type action)"},
    "option": {"type": "string", "description": "option to choose (select action)"},
    "url": {"type": "string", "description": "URL to open (navigate action)"},
    "dir": {"type": "string", "enum": ["up","down"], "description": "scroll direction"},
    "ms": {"type": "integer", "description": "milliseconds to wait (wait action)"},
    "channel": {"type": "string", "enum": ["email","sms","totp"], "description": "verification channel (enter_verification)"},
    "want": {"type": "string", "enum": ["code","link"], "description": "fetch a one-time code or a magic link (enter_verification)"},
    "x": {"type": "integer", "description": "viewport x in CSS px (click_at)"},
    "y": {"type": "integer", "description": "viewport y in CSS px (click_at)"},
    "why": {"type": "string", "description": "one line reason for this action"},
    "reason": {"type": "string", "description": "why you are stopping (done / give_up / request_human)"}
  },
  "required": ["action", "why"],
  "additionalProperties": false
}`, enumJSON))
}

// llmCrawl is the LLM-driven driver: one model-chosen action per step.
func (e *Explorer) llmCrawl(ctx context.Context) (*Report, error) {
	if e.Perceiver == nil {
		e.Perceiver = perceive.New()
	}
	if e.Model == nil {
		return nil, fmt.Errorf("explore: llm driver needs a Model")
	}
	if e.PerAction == 0 {
		e.PerAction = 8 * time.Second
	}
	maxSteps := e.MaxSteps
	if maxSteps == 0 {
		maxSteps = 30
	}
	if e.MaxNoNew == 0 {
		e.MaxNoNew = 6
	}
	rep := &Report{Driver: string(DriverLLM)}
	ex := e.executor()
	mem := &memory{}
	base := basePrefix(e.Page.URL())
	noNew := 0
	loopCount := map[string]int{}
	stuckSkips := 0

	for step := 1; step <= maxSteps; step++ {
		if e.Budget != nil {
			if err := e.Budget.Check(core.ResSteps, 1); err != nil {
				rep.StopReason = "budget"
				break
			}
			e.Budget.Spend(core.ResSteps, 1)
		}
		st, hash, err := e.perceive(ctx)
		if err != nil {
			continue
		}
		if !e.ensureAuthed(ctx, st, rep) {
			rep.StopReason = "auth_lost"
			break
		}
		// Track coverage.
		if !mem.visited[string(hash)] {
			mem.markVisited(string(hash), st.Title, pathTemplate(st.URL))
			rep.StatesVisited++
			tmpl := pathTemplate(st.URL)
			if !containsStr(rep.URLTemplates, tmpl) {
				rep.URLTemplates = append(rep.URLTemplates, tmpl)
			}
			noNew = 0
			stuckSkips = 0 // reaching a new state is real progress
		} else {
			noNew++
			if noNew >= e.MaxNoNew {
				rep.StopReason = "plateau"
				break
			}
		}

		obs := e.observe(st, hash, mem, maxSteps-step, stuckSkips > 0)
		act, usage, visual := e.decideStep(ctx, obs, st)
		_ = visual
		if usage != nil {
			rep.InTokens += usage.InputTokens
			rep.OutTokens += usage.OutputTokens
			rep.CostUSD += usage.CostUSD
		}
		if act.Action == "note" && (strings.Contains(act.Why, "unparseable") || strings.Contains(act.Why, "model error")) {
			mem.note(`Your last reply was not a valid action. Respond with exactly ONE JSON object and nothing else, e.g. {"action":"click","id":12,"why":"..."}.`)
		}
		rep.Steps = step

		ts := TraceStep{N: step, Action: act.Action, Why: act.Why, URL: st.URL, StateID: shortHash(hash)}
		if usage != nil {
			ts.InTokens, ts.OutTokens = usage.InputTokens, usage.OutputTokens
		}

		// Loop detection on (state, action, id): skip a control ONLY once it has
		// actually produced no effect here. A control that shows a message or
		// navigates is never a dead end — re-submitting it is legitimate (e.g. a
		// login form that returns a validation error then succeeds, whose error
		// re-render hashes to the same state). Marking a control dead only after a
		// true no-op avoids skipping the decisive re-submit, while a genuinely inert
		// control is still skipped on the next pick. A run of consecutive dead picks
		// stops the crawl; plateau bounds genuine no-progress.
		lk := ""
		if act.Action == "click" || act.Action == "type" || act.Action == "select" || act.Action == "submit" {
			lk = string(hash) + "|" + act.Action + fmt.Sprint(act.ID)
			if loopCount[lk] >= 1 { // previously executed here and did nothing
				stuckSkips++
				mem.note(fmt.Sprintf("Element %d via %s here is a dead end (no new effect); do NOT pick it again — choose a different element or navigate elsewhere.", act.ID, act.Action))
				ts.Outcome = "loop_skip"
				rep.Trace = append(rep.Trace, ts)
				mem.record(act, "skipped: dead-end element")
				if stuckSkips >= 6 {
					rep.StopReason = "loop"
					break
				}
				continue
			}
		}

		done, outcome := e.applyAction(ctx, ex, st, act, &ts, rep, base, mem)
		if lk != "" && outcome == "no visible change" {
			loopCount[lk]++ // mark this control dead here only if it did nothing
		}
		if outcome != "no visible change" {
			stuckSkips = 0
		}
		// Track stalled progress so visual mode can escalate. A no-op, a login-form
		// fill that found no fields, or an error all count as no progress; a
		// navigation, a shown message, a successful fill/verify reset it.
		if isNoProgress(act.Action, outcome) {
			e.noProgressStreak++
		} else {
			e.noProgressStreak = 0
		}
		// Once a login form is filled/typed, the form-finding phase is over: later
		// interstitials/verification pages must not trigger visual escalation.
		if actAttemptsLogin(act) || strings.Contains(outcome, "login form: filled") || strings.Contains(outcome, "signup form:") {
			e.loginAttempted = true
		}
		mem.record(act, outcome)
		rep.Trace = append(rep.Trace, ts)
		if done {
			break
		}
		_ = e.Page.WaitStable(ctx, e.PerAction)
	}
	if rep.StopReason == "" {
		rep.StopReason = "steps"
	}
	return rep, nil
}

// applyAction executes one action and returns (done, outcome).
func (e *Explorer) applyAction(ctx context.Context, ex *Executor, st perceive.State, act llmAction, ts *TraceStep, rep *Report, base string, mem *memory) (bool, string) {
	before := st.URL
	switch act.Action {
	case "done":
		rep.StopReason = "model_done"
		ts.Outcome = "done"
		if act.Reason != "" {
			rep.Notes = append(rep.Notes, "done: "+act.Reason)
		}
		return true, "done"
	case "note":
		ts.Outcome = "note"
		if act.Why != "" {
			rep.Notes = append(rep.Notes, act.Why)
		}
		return false, "note"
	case "wait":
		d := time.Duration(clampInt(act.Ms, 0, 5000)) * time.Millisecond
		if d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
			}
		}
		ts.Outcome = "waited"
		return false, "no_change"
	case "navigate":
		ts.URL = act.URL
		abs := resolveURL(before, act.URL)
		if act.URL == "" || !strings.HasPrefix(abs, base) {
			ts.Outcome = "off_scope"
			return false, "off_scope (stay within the application)"
		}
		err := ex.Navigate(ctx, abs, act.Why)
		if b, ok := err.(*Blocked); ok {
			ex.recordDenied(rep, "", act.URL)
			ts.Outcome = "blocked:" + b.Category
			return false, "blocked: " + b.Category
		}
		return false, e.outcomeFrom(ctx, err, ts, before)
	case "back":
		return false, e.outcomeFrom(ctx, ex.Back(ctx), ts, before)
	case "scroll":
		return false, e.outcomeFrom(ctx, ex.Scroll(ctx, act.Dir), ts, before)
	case "look":
		// A fresh set-of-marks screenshot is taken on the NEXT decide (visual mode
		// is already active). This action just yields the turn for that.
		e.visualActive = true
		ts.Outcome = "looked"
		return false, "took a screenshot"
	case "click_at":
		if err := ex.ClickAt(ctx, act.X, act.Y, act.Why); err != nil {
			return false, e.outcomeFrom(ctx, err, ts, before)
		}
		return false, e.outcomeFrom(ctx, nil, ts, before)
	case "type_text":
		if err := ex.TypeText(ctx, act.Text, act.Why); err != nil {
			ts.Outcome = "type_text_error"
			return false, "type_text error: " + err.Error()
		}
		ts.Outcome = "typed"
		return false, "typed into the focused field"
	case "click", "submit", "type", "select":
		n := findNode(st, act.ID)
		if n == nil {
			ts.Outcome = "no_such_element"
			return false, fmt.Sprintf("no element with id %d on this page", act.ID)
		}
		ts.TargetID = act.ID
		// In visual mode a marked control is acted on by its rect centre (ClickAt),
		// so a node inside an open shadow root — not resolvable by a flat selector —
		// is still actionable.
		if e.visualActive && (act.Action == "click" || act.Action == "submit") {
			if cx, cy, ok := n.CenterOf(); ok {
				err := ex.ClickAt(ctx, cx, cy, act.Why)
				if err == nil && isSubmitLike(n) {
					rep.FormsSubmitted++
				}
				return false, e.outcomeFrom(ctx, err, ts, before)
			}
		}
		var err error
		switch act.Action {
		case "click":
			err = ex.Click(ctx, n, act.Why)
			// A click on a submit/save button is a form submission too (the model
			// often uses click rather than the submit action).
			if err == nil && isSubmitLike(n) {
				rep.FormsSubmitted++
			}
		case "submit":
			err = ex.Submit(ctx, n, act.Why)
			if err == nil {
				rep.FormsSubmitted++
			}
		case "type":
			err = ex.Type(ctx, n, act.Text, act.Why)
		case "select":
			err = ex.Select(ctx, n, act.Option, act.Why)
		}
		if b, ok := err.(*Blocked); ok {
			ex.recordDenied(rep, nodeLabel(n), n.Href)
			ts.Outcome = "blocked:" + b.Category
			return false, "blocked: " + b.Category
		}
		return false, e.outcomeFrom(ctx, err, ts, before)
	case "enter_verification":
		if e.EnterVerification == nil {
			ts.Outcome = "unknown_action"
			return false, "enter_verification is not available"
		}
		n := findNode(st, act.ID) // optional hint; the callback re-detects the field
		if n != nil {
			ts.TargetID = act.ID
		}
		outcome, err := e.EnterVerification(ctx, n, strings.ToLower(act.Channel), strings.EqualFold(act.Want, "link"))
		if err != nil {
			ts.Outcome = "verify_error"
			return false, "verification error: " + err.Error()
		}
		ts.Outcome = "entered_verification"
		// The code/link itself is never surfaced — only the outcome label.
		return false, "verification: " + outcome
	case "resend_verification":
		if e.ResendVerification == nil {
			ts.Outcome = "unknown_action"
			return false, "resend_verification is not available"
		}
		n := findNode(st, act.ID)
		if n != nil {
			ts.TargetID = act.ID
		}
		if err := e.ResendVerification(ctx, n); err != nil {
			ts.Outcome = "resend_error"
			return false, "resend error: " + err.Error()
		}
		ts.Outcome = "resent"
		return false, "a fresh verification code was requested"
	case "solve_captcha":
		if e.SolveCaptcha == nil {
			ts.Outcome = "unknown_action"
			return false, "solve_captcha is not available"
		}
		outcome, err := e.SolveCaptcha(ctx)
		if err != nil {
			ts.Outcome = "captcha_error"
			return false, "captcha error: " + err.Error()
		}
		ts.Outcome = "captcha:" + outcome
		return false, "captcha: " + outcome
	case "check_logged_in":
		if e.CheckLoggedIn == nil {
			ts.Outcome = "unknown_action"
			return false, "check_logged_in is not available"
		}
		ok, ev := e.CheckLoggedIn(ctx)
		if ok {
			rep.StopReason = "logged_in"
			rep.Notes = append(rep.Notes, "check_logged_in: authenticated ("+strings.Join(ev, ", ")+")")
			ts.Outcome = "logged_in"
			return true, "logged in: " + strings.Join(ev, ", ")
		}
		ts.Outcome = "not_logged_in"
		return false, "not logged in yet — keep going"
	case "request_human":
		if e.RequestHuman == nil {
			ts.Outcome = "unknown_action"
			return false, "request_human is not available"
		}
		reason := firstNonEmpty(act.Reason, act.Why)
		err := e.RequestHuman(ctx, reason)
		rep.StopReason = "request_human"
		if reason != "" {
			rep.Notes = append(rep.Notes, "request_human: "+reason)
		}
		ts.Outcome = "request_human"
		if err != nil {
			return true, "human handoff error: " + err.Error()
		}
		return true, "escalated to a human"
	case "fill_login_form":
		if e.FillLoginForm == nil {
			ts.Outcome = "unknown_action"
			return false, "fill_login_form is not available"
		}
		n := findNode(st, act.ID) // the form (or a field in it); callback re-detects
		if n != nil {
			ts.TargetID = act.ID
		}
		outcome, err := e.FillLoginForm(ctx, n)
		if err != nil {
			ts.Outcome = "fill_error"
			return false, "fill login form error: " + err.Error()
		}
		ts.Outcome = "filled_login"
		return false, "login form: " + outcome
	case "fill_signup_form":
		if e.FillSignupForm == nil {
			ts.Outcome = "unknown_action"
			return false, "fill_signup_form is not available"
		}
		n := findNode(st, act.ID)
		if n != nil {
			ts.TargetID = act.ID
		}
		outcome, err := e.FillSignupForm(ctx, n)
		if err != nil {
			ts.Outcome = "fill_error"
			return false, "fill signup form error: " + err.Error()
		}
		ts.Outcome = "filled_signup"
		return false, "signup form: " + outcome
	case "dismiss_banner":
		if e.DismissBanner == nil {
			ts.Outcome = "unknown_action"
			return false, "dismiss_banner is not available"
		}
		outcome, err := e.DismissBanner(ctx)
		if err != nil {
			ts.Outcome = "banner_error"
			return false, "dismiss banner error: " + err.Error()
		}
		ts.Outcome = "banner:" + outcome
		return false, "banner: " + outcome
	case "give_up":
		rep.StopReason = "give_up"
		if r := firstNonEmpty(act.Reason, act.Why); r != "" {
			rep.Notes = append(rep.Notes, "give_up: "+r)
		}
		ts.Outcome = "give_up"
		return true, "gave up"
	default:
		ts.Outcome = "unknown_action"
		return false, "unknown action"
	}
}

// outcomeFrom classifies the result of an action by re-perceiving. A click or
// submit can trigger a form POST whose re-render (an error/success message, often
// at the SAME URL) lags the action: WaitStable may return on the pre-POST DOM. So
// when the first read shows no change, settle and re-perceive once more before
// concluding nothing happened — otherwise the loop detector wrongly marks a
// working submit control as a dead end and skips the decisive re-submit.
func (e *Explorer) outcomeFrom(ctx context.Context, actErr error, ts *TraceStep, beforeURL string) string {
	if actErr != nil {
		ts.Outcome = "error"
		return "error: " + actErr.Error()
	}
	_ = e.Page.WaitStable(ctx, e.PerAction)
	st, _, err := e.perceive(ctx)
	if err != nil {
		ts.Outcome = "ok"
		return "ok"
	}
	if msg := pageMessage(st); msg == "" && st.URL == beforeURL {
		// Possible lagging POST re-render: give it one more bounded settle.
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
		}
		_ = e.Page.WaitStable(ctx, e.PerAction)
		if st2, _, err2 := e.perceive(ctx); err2 == nil {
			st = st2
		}
	}
	if msg := pageMessage(st); msg != "" {
		ts.Outcome = "message_shown"
		return "message shown: " + msg
	}
	if st.URL != beforeURL {
		ts.Outcome = "navigated"
		return "navigated to " + pathTemplate(st.URL)
	}
	ts.Outcome = "no_change"
	return "no visible change"
}

// recordDenied appends a denied-action record (shared shape with BFS).
func (ex *Executor) recordDenied(rep *Report, label, href string) {
	rep.Denied = append(rep.Denied, DeniedAction{Label: label, Href: href, Reason: "account_safety"})
}

// decide asks the model for one action, defaulting to a safe note on failure.
func (e *Explorer) decide(ctx context.Context, obs string) (llmAction, *model.Usage) {
	sys := e.systemPrompt()
	msgs := []model.Message{
		{Role: model.System, Text: sys},
		{Role: model.User, Text: obs},
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, usage, err := e.Model.Structured(cctx, msgs, e.buildSchema(), model.ChatOpts{MaxTokens: 1024})
	if err != nil {
		e.crawlDebug("model error: %v", err)
		return llmAction{Action: "note", Why: "model error: " + err.Error()}, usage
	}
	a, ok := parseAction(raw)
	e.crawlDebug("raw=%s parsed=%+v ok=%v", strings.TrimSpace(string(raw)), a, ok)
	if !ok {
		return llmAction{Action: "note", Why: "unparseable action"}, usage
	}
	return a, usage
}

func (e *Explorer) crawlDebug(format string, args ...any) {
	if os.Getenv("AGENTBROWSER_CRAWL_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "[crawl] "+format+"\n", args...)
}

// parseAction decodes one action tolerantly: it accepts a bare action object or
// a common wrapper ({"action":{…}}, {"tool":…,"parameters":{…}},
// {"name":…,"arguments":{…}}), and coerces id/ms given as strings.
func parseAction(raw []byte) (llmAction, bool) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return llmAction{}, false
	}
	// Unwrap tool-call shapes some models emit.
	if name, ok := m["name"].(string); ok {
		if args, ok := m["arguments"].(map[string]any); ok {
			args["action"] = firstStr(args["action"], name)
			m = args
		}
	} else if name, ok := m["tool"].(string); ok {
		if args, ok := m["parameters"].(map[string]any); ok {
			args["action"] = firstStr(args["action"], name)
			m = args
		}
	} else if inner, ok := m["action"].(map[string]any); ok {
		m = inner
	}
	a := llmAction{
		Action:  jstr(m["action"]),
		Text:    jstr(m["text"]),
		Option:  jstr(m["option"]),
		URL:     jstr(m["url"]),
		Dir:     jstr(m["dir"]),
		Why:     jstr(m["why"]),
		Reason:  jstr(m["reason"]),
		Channel: jstr(m["channel"]),
		Want:    jstr(m["want"]),
		ID:      jint(m["id"]),
		Ms:      jint(m["ms"]),
		X:       jint(m["x"]),
		Y:       jint(m["y"]),
	}
	// Some models pass the target field as "element" rather than "id".
	if a.ID == 0 {
		a.ID = jint(m["element"])
	}
	return a, a.Action != ""
}

func jstr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func jint(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		x := 0
		for _, c := range n {
			if c < '0' || c > '9' {
				break
			}
			x = x*10 + int(c-'0')
		}
		return x
	}
	return 0
}

func firstStr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func (e *Explorer) systemPrompt() string {
	tmpl := e.SystemPrompt
	if tmpl == "" {
		tmpl = defaultSystemPrompt
	}
	op := strings.TrimSpace(e.Prompt)
	if op == "" {
		op = "(none — explore broadly on your own judgement.)"
	}
	return strings.ReplaceAll(tmpl, "<<OPERATOR>>", op)
}

func (e *Explorer) executor() *Executor {
	return &Executor{
		Page: e.Page, Audit: e.Audit, Secrets: e.Secrets, Timeout: e.PerAction,
		Flag: func(field, kind string) {},
		Log:  func(kind string, payload map[string]any) { e.log(kind, payload) },
	}
}

// ensureAuthed checks the session and re-logs-in once if it dropped.
func (e *Explorer) ensureAuthed(ctx context.Context, st perceive.State, rep *Report) bool {
	if e.IsAuthed == nil || e.IsAuthed(ctx, e.Page, st) {
		return true
	}
	if e.Relogin == nil {
		return false
	}
	e.log("relogin", map[string]any{"at": st.URL})
	if err := e.Relogin(ctx); err != nil {
		return false
	}
	rep.Relogins++
	return true
}

var submitNameRe = regexp.MustCompile(`(?i)\b(save|submit|post|send|update|create|publish|apply|confirm|sign ?up|register)\b`)

// isSubmitLike reports whether a node is a form submit/save control.
func isSubmitLike(n *perceive.Node) bool {
	if n.Type == "submit" {
		return true
	}
	if n.Tag != "button" && strings.ToLower(n.Role) != "button" {
		return false
	}
	return submitNameRe.MatchString(nodeLabel(n))
}

func findNode(st perceive.State, id int) *perceive.Node {
	for i := range st.Nodes {
		if st.Nodes[i].Handle == id {
			return &st.Nodes[i]
		}
	}
	return nil
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func shortHash(h perceive.StateHash) string {
	s := string(h)
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
