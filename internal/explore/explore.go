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
	StatesVisited  int            `json:"states_visited"`
	URLTemplates   []string       `json:"url_templates"`
	FormsSubmitted int            `json:"forms_submitted"`
	Denied         []DeniedAction `json:"denied_actions"`
	Relogins       int            `json:"relogins"`
	StopReason     string         `json:"stop_reason"` // budget | plateau | frontier_empty | auth_lost
	Pages          int            `json:"pages"`
}

// AuthCheck reports whether the current page still looks authenticated.
type AuthCheck func(ctx context.Context, p *browser.Page, st perceive.State) bool

// Relogin re-establishes the session (and reloads) when it drops mid-crawl.
type Relogin func(ctx context.Context) error

// Explorer crawls an authenticated app like a normal user.
type Explorer struct {
	Page        *browser.Page
	Perceiver   perceive.Perceiver
	Budget      core.Budget
	Audit       audit.AuditLog
	IsAuthed    AuthCheck
	Relogin     Relogin
	MaxNoNew    int           // stop after this many steps with no new state (default 4)
	MaxPages    int           // hard cap on page visits (default 60)
	SubmitForms bool          // fill+submit a representative form per state (default true)
	PerAction   time.Duration // per-action timeout (default 8s)
}

// Crawl runs the authenticated exploration starting from the page's current
// location. It returns a Report. It never clicks an account-safety control.
func (e *Explorer) Crawl(ctx context.Context) (*Report, error) {
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
	rep := &Report{}
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

		// Exercise a representative form (once per template).
		if e.submitForms() && !submittedTmpl[tmpl] {
			if e.trySubmitForm(ctx, st, rep) {
				submittedTmpl[tmpl] = true
				// Re-perceive after submit (state may have changed).
				st, _, _ = e.perceive(ctx)
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

func (e *Explorer) submitForms() bool   { return e.SubmitForms || !e.formsDisabled() }
func (e *Explorer) formsDisabled() bool { return false }

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
