package explore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// DeniedAction records an account-safety control the crawl refused to use.
type DeniedAction struct {
	Label  string `json:"label"`
	Href   string `json:"href,omitempty"`
	Reason string `json:"reason"`
}

// Report summarises a crawl.
type Report struct {
	Driver         string         `json:"driver,omitempty"` // bfs | llm | hybrid
	StatesVisited  int            `json:"states_visited"`
	URLTemplates   []string       `json:"url_templates"`
	FormsSubmitted int            `json:"forms_submitted"`
	Denied         []DeniedAction `json:"denied_actions"`
	Relogins       int            `json:"relogins"`
	StopReason     string         `json:"stop_reason"` // budget | plateau | frontier_empty | auth_lost | model_done | steps | loop
	Pages          int            `json:"pages"`
	// LLM/hybrid driver accounting.
	Steps       int         `json:"steps,omitempty"`
	Screenshots int         `json:"screenshots,omitempty"` // visual-mode screenshots taken
	InTokens    int         `json:"in_tokens,omitempty"`
	OutTokens   int         `json:"out_tokens,omitempty"`
	CostUSD     float64     `json:"cost_usd,omitempty"`
	Notes       []string    `json:"notes,omitempty"`
	Flags       []string    `json:"flags,omitempty"` // foreign identity values etc.
	Trace       []TraceStep `json:"-"`               // written to a file by the caller
}

// TraceStep is one LLM-driver step for the written step trace.
type TraceStep struct {
	N         int    `json:"n"`
	Action    string `json:"action"`
	Why       string `json:"why,omitempty"`
	TargetID  int    `json:"target_id,omitempty"`
	URL       string `json:"url,omitempty"`
	Outcome   string `json:"outcome"`
	StateID   string `json:"state_id,omitempty"`
	InTokens  int    `json:"in_tokens,omitempty"`
	OutTokens int    `json:"out_tokens,omitempty"`
}

// AuthCheck reports whether the current page still looks authenticated.
type AuthCheck func(ctx context.Context, p browser.Page, st perceive.State) bool

// Relogin re-establishes the session (and reloads) when it drops mid-crawl.
type Relogin func(ctx context.Context) error

// Driver selects how the crawl decides what to do next.
type Driver string

// Crawl drivers.
const (
	DriverBFS    Driver = "bfs"    // deterministic breadth-first link following
	DriverLLM    Driver = "llm"    // an LLM chooses one action per step
	DriverHybrid Driver = "hybrid" // BFS breadth + LLM for forms/ambiguous controls
)

// Explorer crawls an authenticated app like a normal user.
type Explorer struct {
	Page        browser.Page
	Perceiver   perceive.Perceiver
	Budget      core.Budget
	Audit       audit.AuditLog
	IsAuthed    AuthCheck
	Relogin     Relogin
	MaxNoNew    int           // stop after this many steps with no new state (default 4)
	MaxPages    int           // hard cap on page visits (default 60)
	SubmitForms bool          // fill+submit a representative form per state (default true)
	PerAction   time.Duration // per-action timeout (default 8s)

	// Driver selects bfs (default), llm or hybrid.
	Driver Driver
	// Model is required for the llm and hybrid drivers.
	Model LLMModel
	// VisionModel drives visual-mode steps (viewport screenshot + set-of-marks).
	// If nil, visual steps fall back to Model — which only works if Model is a
	// vision-capable provider. Wire a vision-capable provider here (e.g. the plan
	// model) so visual escalation can read closed-shadow/canvas pages.
	VisionModel LLMModel
	// MaxScreenshots caps visual-mode screenshots per run (default 10).
	MaxScreenshots int
	// Secrets are the account values the Executor substitutes for placeholders.
	Secrets Secrets
	// Prompt is the operator's free-text instruction (llm/hybrid).
	Prompt string
	// SystemPrompt overrides the embedded system template (optional).
	SystemPrompt string
	// MaxSteps caps LLM-driver steps (default 30).
	MaxSteps int
	// ObsTokenBudget bounds the observation size (default ~2000 tokens).
	ObsTokenBudget int

	// --- agent-takeover tools (optional) ------------------------------------
	// When any of these callbacks is set, the LLM driver is in "takeover" mode:
	// the extra verification/captcha/success/human actions are advertised to the
	// model (only the ones whose callback is non-nil) and dispatched in
	// applyAction. They let the agent finish a login the deterministic flow could
	// not, reusing the caller's existing verifier / captcha / success / handoff
	// machinery. The callbacks never return or surface a real secret (an OTP code,
	// a magic link) to the model — only an outcome label.

	// EnterVerification fetches the current verification code/link from the
	// configured channel (honouring since/claims/alias) and enters it. n is the
	// model-chosen target field (may be nil; the callback re-detects the field).
	// x,y are an optional viewport coordinate (CSS px) for VISUAL entry when the
	// code field is not perceivable (closed shadow root) — 0,0 means none; the
	// callback focuses that point and types the code into focus. It returns a short
	// outcome label, never the code.
	EnterVerification func(ctx context.Context, n *perceive.Node, channel string, wantLink bool, x, y int) (string, error)
	// ResendVerification triggers a fresh code (clicks the resend control, bumps
	// the since window). n is the model-chosen resend control (may be nil).
	ResendVerification func(ctx context.Context, n *perceive.Node) error
	// SolveCaptcha runs the solver chain on the current page. Returns an outcome
	// label ("solved", "no captcha", …).
	SolveCaptcha func(ctx context.Context) (string, error)
	// CheckLoggedIn runs success detection and returns (authenticated, evidence).
	// This is the ONLY gate on takeover success: the model saying "done" is not
	// enough.
	CheckLoggedIn func(ctx context.Context) (bool, []string)
	// RequestHuman escalates to the human handoff with the model's reason.
	RequestHuman func(ctx context.Context, reason string) error

	// FillLoginForm fills the login form the model points at (by element id) with
	// the account's username/password via placeholders the Executor substitutes —
	// the real values never reach the model. Returns an outcome label. The model
	// still decides when to submit (a separate submit/click action).
	FillLoginForm func(ctx context.Context, n *perceive.Node) (string, error)
	// FillSignupForm is FillLoginForm for a registration form.
	FillSignupForm func(ctx context.Context, n *perceive.Node) (string, error)
	// DismissBanner dismisses a cookie/consent banner on the current page (the
	// privacy-preserving choice). Returns an outcome label.
	DismissBanner func(ctx context.Context) (string, error)

	// hybrid is set by hybridCrawl so the shared BFS loop hands forms to the LLM.
	hybrid bool

	// --- visual-mode runtime state (llm driver) -----------------------------
	visualActive     bool // currently escalated to screenshot-driven perception
	screenshotsUsed  int  // screenshots taken this run (capped by MaxScreenshots)
	noProgressStreak int  // consecutive steps that produced no visible change
	loginAttempted   bool // the agent has filled/typed a login form at least once
	fillFoundNoForm  bool // the last fill reported no readable email/password fields
}

// takeoverMode reports whether any agent-takeover tool is wired, enabling the
// extra actions in the schema and applyAction.
func (e *Explorer) takeoverMode() bool {
	return e.EnterVerification != nil || e.ResendVerification != nil ||
		e.SolveCaptcha != nil || e.CheckLoggedIn != nil || e.RequestHuman != nil ||
		e.FillLoginForm != nil || e.FillSignupForm != nil || e.DismissBanner != nil
}

// Crawl runs the authenticated exploration starting from the page's current
// location, dispatching on Driver. It never takes an account-safety action.
func (e *Explorer) Crawl(ctx context.Context) (*Report, error) {
	switch e.Driver {
	case DriverLLM:
		return e.llmCrawl(ctx)
	case DriverHybrid:
		return e.hybridCrawl(ctx)
	default:
		return e.bfsCrawl(ctx)
	}
}

// bfsCrawl is the deterministic breadth-first driver.
func (e *Explorer) bfsCrawl(ctx context.Context) (*Report, error) {
	if e.Perceiver == nil {
		e.Perceiver = perceive.New()
	}
	if e.MaxNoNew == 0 {
		e.MaxNoNew = 4
	}
	if e.MaxPages == 0 {
		e.MaxPages = 60
	}
	if e.PerAction == 0 {
		e.PerAction = 8 * time.Second
	}
	rep := &Report{Driver: string(DriverBFS)}
	start := e.Page.URL()
	base := basePrefix(start)
	visitedHash := map[perceive.StateHash]bool{}
	visitedTmpl := map[string]bool{}
	submittedTmpl := map[string]bool{}
	queued := map[string]bool{start: true}
	frontier := []string{start}
	noNew := 0

	for len(frontier) > 0 {
		if e.Budget != nil {
			if err := e.Budget.Check(core.ResSteps, 1); err != nil {
				rep.StopReason = "budget"
				return rep, nil
			}
			e.Budget.Spend(core.ResSteps, 1)
		}
		if rep.Pages >= e.MaxPages {
			rep.StopReason = "budget"
			return rep, nil
		}
		u := frontier[0]
		frontier = frontier[1:]

		if err := e.navigate(ctx, u); err != nil {
			continue
		}
		rep.Pages++
		st, hash, err := e.perceive(ctx)
		if err != nil {
			continue
		}
		// Session still alive?
		if e.IsAuthed != nil && !e.IsAuthed(ctx, e.Page, st) {
			if e.Relogin == nil {
				rep.StopReason = "auth_lost"
				return rep, nil
			}
			e.log("relogin", map[string]any{"at": u})
			if err := e.Relogin(ctx); err != nil {
				rep.StopReason = "auth_lost"
				return rep, nil
			}
			rep.Relogins++
			// Restart this sub-goal: re-visit the same URL.
			if err := e.navigate(ctx, u); err != nil {
				continue
			}
			st, hash, err = e.perceive(ctx)
			if err != nil {
				continue
			}
		}

		tmpl := pathTemplate(st.URL)
		isNew := !visitedHash[hash] || !visitedTmpl[tmpl]
		if isNew {
			noNew = 0
			if !visitedHash[hash] {
				rep.StatesVisited++
			}
			if !visitedTmpl[tmpl] {
				visitedTmpl[tmpl] = true
				rep.URLTemplates = append(rep.URLTemplates, tmpl)
			}
			visitedHash[hash] = true
		} else {
			noNew++
			if noNew >= e.MaxNoNew {
				rep.StopReason = "plateau"
				return rep, nil
			}
		}

		// Exercise a representative form (once per template). In hybrid mode an
		// LLM fills and submits it (richer, context-aware data); otherwise the
		// deterministic form filler runs.
		if e.SubmitForms && !submittedTmpl[tmpl] {
			done := false
			if e.hybrid && e.Model != nil && stateHasForm(st) {
				done = e.llmFillForm(ctx, st, rep)
			} else {
				done = e.trySubmitForm(ctx, st, rep)
			}
			if done {
				submittedTmpl[tmpl] = true
				st, _, _ = e.perceive(ctx) // re-perceive after submit
			}
		}

		// Enqueue new first-party links under the base prefix, skipping
		// account-safety controls (deny-list at the action boundary).
		for _, n := range st.Nodes {
			if !n.Visible || n.Tag != "a" || n.Href == "" {
				continue
			}
			label := n.Name
			if label == "" {
				label = n.Text
			}
			if reason := DenyReason(label, n.Href); reason != "" {
				e.recordDenied(rep, label, n.Href, reason)
				continue
			}
			abs := resolveURL(st.URL, n.Href)
			if abs == "" || !strings.HasPrefix(abs, base) {
				continue
			}
			key := perceive.PathTemplate(abs) + "?" + queryKey(abs)
			if queued[abs] || queued[key] {
				continue
			}
			queued[abs] = true
			queued[key] = true
			frontier = append(frontier, abs)
		}
	}
	if rep.StopReason == "" {
		rep.StopReason = "frontier_empty"
	}
	return rep, nil
}

// navigate goes to a URL, bounded by PerAction. Account-safety hrefs are never
// enqueued, so navigation targets are already filtered.
func (e *Explorer) navigate(ctx context.Context, u string) error {
	cctx, cancel := context.WithTimeout(ctx, e.PerAction+4*time.Second)
	defer cancel()
	return e.Page.Navigate(cctx, u)
}

func (e *Explorer) perceive(ctx context.Context) (perceive.State, perceive.StateHash, error) {
	cctx, cancel := context.WithTimeout(ctx, e.PerAction)
	defer cancel()
	return e.Perceiver.Perceive(cctx, e.Page)
}

// trySubmitForm fills a visible form with sensible typed data and submits it,
// provided its submit control is not an account-safety action.
func (e *Explorer) trySubmitForm(ctx context.Context, st perceive.State, rep *Report) bool {
	var submit *perceive.Node
	var inputs []perceive.Node
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if !n.Visible || n.Disabled {
			continue
		}
		switch {
		case n.Tag == "input" && (n.Type == "text" || n.Type == "email" || n.Type == "search" || n.Type == "tel" || n.Type == "number" || n.Type == "url" || n.Type == ""):
			inputs = append(inputs, *n)
		case n.Tag == "textarea":
			inputs = append(inputs, *n)
		case (n.Tag == "button" && (n.Type == "submit" || n.Type == "")) || (n.Tag == "input" && n.Type == "submit"):
			label := n.Name
			if label == "" {
				label = n.Text
			}
			if DenyReason(label, "") != "" {
				e.recordDenied(rep, label, "", "submit:account_safety")
				continue
			}
			if submit == nil {
				submit = n
			}
		}
	}
	if submit == nil || len(inputs) == 0 {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, e.PerAction)
	defer cancel()
	for i := range inputs {
		n := inputs[i]
		el, err := e.Page.Element(cctx, nil, perceive.Selector(n.Handle), e.PerAction)
		if err != nil {
			continue
		}
		_ = e.Page.TypeInto(cctx, el, sampleValue(n))
	}
	sel, err := e.Page.Element(cctx, nil, perceive.Selector(submit.Handle), e.PerAction)
	if err != nil {
		return false
	}
	if err := e.Page.Click(cctx, sel); err != nil {
		return false
	}
	_ = e.Page.WaitStable(ctx, browser.DefaultStableTimeout)
	rep.FormsSubmitted++
	e.log("form_submit", map[string]any{"url": st.URL, "fields": len(inputs)})
	return true
}

func (e *Explorer) recordDenied(rep *Report, label, href, reason string) {
	for _, d := range rep.Denied {
		if d.Label == label && d.Href == href {
			return
		}
	}
	rep.Denied = append(rep.Denied, DeniedAction{Label: label, Href: href, Reason: reason})
	e.log("deny_list", map[string]any{"label": label, "href": href, "reason": reason})
}

func (e *Explorer) log(kind string, payload map[string]any) {
	if e.Audit != nil {
		_, _ = e.Audit.Append(context.Background(), audit.Entry{Actor: "explorer", Kind: kind, Payload: payload})
	}
}

// sampleValue returns sensible typed data for an input.
func sampleValue(n perceive.Node) string {
	label := strings.ToLower(n.Name + " " + n.Placeholder + " " + n.NameAttr + " " + n.ID)
	switch {
	case n.Type == "email" || strings.Contains(label, "email"):
		return "alex.taylor@example.com"
	case n.Type == "number" || strings.Contains(label, "age") || strings.Contains(label, "qty"):
		return "7"
	case n.Type == "tel" || strings.Contains(label, "phone"):
		return "+15555550123"
	case n.Type == "url" || strings.Contains(label, "url") || strings.Contains(label, "website"):
		return "https://example.com"
	case strings.Contains(label, "search") || n.Type == "search":
		return "widget"
	case strings.Contains(label, "name"):
		return "Alex Taylor"
	case n.Tag == "textarea" || strings.Contains(label, "message") || strings.Contains(label, "bio") || strings.Contains(label, "note"):
		return "Updated by the automated crawler."
	default:
		return "test value"
	}
}

func basePrefix(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	path := parsed.Path
	// Keep the first path segment as the app base (e.g. /crawl/).
	if i := strings.Index(strings.TrimPrefix(path, "/"), "/"); i >= 0 {
		path = "/" + strings.TrimPrefix(path, "/")[:i] + "/"
	}
	return fmt.Sprintf("%s://%s%s", parsed.Scheme, parsed.Host, path)
}

func resolveURL(baseURL, href string) string {
	b, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	r, err := url.Parse(href)
	if err != nil {
		return ""
	}
	res := b.ResolveReference(r)
	res.Fragment = ""
	return res.String()
}

// pathTemplate returns the path of u with id-like segments collapsed to {id}
// (host-independent, for the ground-truth state report).
func pathTemplate(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return perceive.PathTemplate(u)
	}
	return perceive.PathTemplate(p.Path)
}

func queryKey(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.RawQuery
}
