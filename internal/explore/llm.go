package explore

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
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
	Action string `json:"action"`
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Option string `json:"option"`
	URL    string `json:"url"`
	Dir    string `json:"dir"`
	Ms     int    `json:"ms"`
	Why    string `json:"why"`
	Reason string `json:"reason"`
}

// actionSchema constrains the model to one action object.
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
		act, usage := e.decide(ctx, obs)
		if usage != nil {
			rep.InTokens += usage.InputTokens
			rep.OutTokens += usage.OutputTokens
			rep.CostUSD += usage.CostUSD
		}
		rep.Steps = step

		ts := TraceStep{N: step, Action: act.Action, Why: act.Why, URL: st.URL, StateID: shortHash(hash)}
		if usage != nil {
			ts.InTokens, ts.OutTokens = usage.InputTokens, usage.OutputTokens
		}

		// Loop detection on (state, action, id): a repeat of the exact same action
		// on the exact same state already produced no new state, so running it
		// again is pointless. Skip it (don't execute), tell the model that element
		// is a dead end, and let it choose something else — a single stuck control
		// must not end an otherwise-productive crawl. Only a run of consecutive
		// dead-end picks (the model truly stuck) stops the crawl; plateau still
		// bounds genuine no-progress.
		if act.Action == "click" || act.Action == "type" || act.Action == "select" || act.Action == "submit" {
			lk := string(hash) + "|" + act.Action + fmt.Sprint(act.ID)
			loopCount[lk]++
			if loopCount[lk] >= 2 {
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
		if outcome != "no visible change" {
			stuckSkips = 0
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
	case "click", "submit", "type", "select":
		n := findNode(st, act.ID)
		if n == nil {
			ts.Outcome = "no_such_element"
			return false, fmt.Sprintf("no element with id %d on this page", act.ID)
		}
		ts.TargetID = act.ID
		var err error
		switch act.Action {
		case "click":
			err = ex.Click(ctx, n, act.Why)
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
	default:
		ts.Outcome = "unknown_action"
		return false, "unknown action"
	}
}

// outcomeFrom classifies the result of an action by re-perceiving.
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
	raw, usage, err := e.Model.Structured(cctx, msgs, actionSchema, model.ChatOpts{MaxTokens: 1024})
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
		Action: jstr(m["action"]),
		Text:   jstr(m["text"]),
		Option: jstr(m["option"]),
		URL:    jstr(m["url"]),
		Dir:    jstr(m["dir"]),
		Why:    jstr(m["why"]),
		Reason: jstr(m["reason"]),
		ID:     jint(m["id"]),
		Ms:     jint(m["ms"]),
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
