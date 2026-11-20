package explore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// Secrets holds the account's real values. They are NEVER placed in the model
// context: the model types placeholders ({{account.password}} etc.) and the
// Executor substitutes the value here, at type time, logging only the
// placeholder. TOTP is a function because the code is time-based.
type Secrets struct {
	Email    string
	Password string
	Phone    string
	TOTP     func() (string, error)
}

// Blocked is returned when an action is refused by the account-safety deny-list.
// The crawl loop reports its Category back to the model as "blocked:<category>".
type Blocked struct{ Category string }

func (b *Blocked) Error() string { return "blocked: " + b.Category }

// Executor performs typed actions against perceived nodes for any crawl driver
// (BFS or LLM). It is the single enforcement point: the account-safety deny-list
// is checked here before every click/submit/navigate, secret placeholders are
// substituted here (never in the model context), and every action is audited.
type Executor struct {
	Page    browser.Page
	Audit   audit.AuditLog
	Secrets Secrets
	Timeout time.Duration
	// Flag records a foreign value typed into an identity (email/phone) field —
	// i.e. a model-invented value that is not the account's own. Optional.
	Flag func(field, placeholderOrKind string)
	// Log records a structured action event. Optional.
	Log func(kind string, payload map[string]any)
}

func (e *Executor) timeout() time.Duration {
	if e.Timeout <= 0 {
		return 8 * time.Second
	}
	return e.Timeout
}

func (e *Executor) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, e.timeout())
}

func (e *Executor) audit(ctx context.Context, kind string, payload map[string]any) {
	if e.Log != nil {
		e.Log(kind, payload)
	}
	if e.Audit != nil {
		_, _ = e.Audit.Append(ctx, audit.Entry{Actor: "executor", Kind: kind, Payload: payload})
	}
}

// Click clicks a node after an account-safety check. A denied control returns a
// *Blocked error.
func (e *Executor) Click(ctx context.Context, n *perceive.Node, why string) error {
	if reason := DenyReason(nodeLabel(n), n.Href); reason != "" {
		e.audit(ctx, "blocked", map[string]any{"action": "click", "handle": n.Handle, "reason": reason})
		return &Blocked{Category: category(reason)}
	}
	return e.rawClick(ctx, n, "click", why)
}

// Submit clicks a submit control (same deny check as Click, kept separate so the
// trace distinguishes a submit from a plain click).
func (e *Executor) Submit(ctx context.Context, n *perceive.Node, why string) error {
	if reason := DenyReason(nodeLabel(n), n.Href); reason != "" {
		e.audit(ctx, "blocked", map[string]any{"action": "submit", "handle": n.Handle, "reason": reason})
		return &Blocked{Category: category(reason)}
	}
	return e.rawClick(ctx, n, "submit", why)
}

func (e *Executor) rawClick(ctx context.Context, n *perceive.Node, kind, why string) error {
	cctx, cancel := e.bound(ctx)
	defer cancel()
	fr, err := perceive.FrameOf(cctx, e.Page, *n)
	if err != nil {
		return err
	}
	el, err := e.Page.Element(cctx, fr, perceive.Selector(n.Handle), e.timeout())
	if err != nil {
		return err
	}
	if err := e.Page.Click(cctx, el); err != nil {
		return err
	}
	e.audit(ctx, "action", map[string]any{"kind": kind, "handle": n.Handle, "label": nodeLabel(n), "why": why})
	return nil
}

// Type substitutes secret placeholders in text and types the result into a node.
// The audit records the placeholder/text as written by the caller (which, for a
// secret, is the placeholder), never the resolved value. A model-invented value
// in an identity field is flagged.
func (e *Executor) Type(ctx context.Context, n *perceive.Node, text, why string) error {
	resolved, usedPlaceholder, err := e.substitute(text)
	if err != nil {
		return err
	}
	if isIdentityField(n) && !usedPlaceholder && !e.isOwnIdentity(resolved) {
		if e.Flag != nil {
			e.Flag(nodeLabel(n), "foreign-value")
		}
		e.audit(ctx, "flag", map[string]any{"kind": "foreign_identity_value", "handle": n.Handle, "field": nodeLabel(n)})
	}
	cctx, cancel := e.bound(ctx)
	defer cancel()
	fr, err := perceive.FrameOf(cctx, e.Page, *n)
	if err != nil {
		return err
	}
	el, err := e.Page.Element(cctx, fr, perceive.Selector(n.Handle), e.timeout())
	if err != nil {
		return err
	}
	if err := e.Page.TypeInto(cctx, el, resolved); err != nil {
		return err
	}
	// Audit the written text, but redact a secret placeholder's shape only (the
	// placeholder itself is safe; the resolved value never appears here).
	e.audit(ctx, "action", map[string]any{"kind": "type", "handle": n.Handle, "text": auditText(text, usedPlaceholder), "why": why})
	return nil
}

// Select picks an option in a <select>.
func (e *Executor) Select(ctx context.Context, n *perceive.Node, option, why string) error {
	cctx, cancel := e.bound(ctx)
	defer cancel()
	fr, err := perceive.FrameOf(cctx, e.Page, *n)
	if err != nil {
		return err
	}
	el, err := e.Page.Element(cctx, fr, perceive.Selector(n.Handle), e.timeout())
	if err != nil {
		return err
	}
	if err := e.Page.SelectOption(cctx, el, option); err != nil {
		return err
	}
	e.audit(ctx, "action", map[string]any{"kind": "select", "handle": n.Handle, "option": option, "why": why})
	return nil
}

// Navigate goes to a URL after an account-safety check on the URL.
func (e *Executor) Navigate(ctx context.Context, url, why string) error {
	if reason := DenyReason("", url); reason != "" {
		e.audit(ctx, "blocked", map[string]any{"action": "navigate", "url": url, "reason": reason})
		return &Blocked{Category: category(reason)}
	}
	cctx, cancel := context.WithTimeout(ctx, e.timeout()+4*time.Second)
	defer cancel()
	if err := e.Page.Navigate(cctx, url); err != nil {
		return err
	}
	e.audit(ctx, "action", map[string]any{"kind": "navigate", "url": url, "why": why})
	return nil
}

// Scroll scrolls the viewport. dir is "up" or "down".
func (e *Executor) Scroll(ctx context.Context, dir string) error {
	cctx, cancel := e.bound(ctx)
	defer cancel()
	by := "window.innerHeight*0.8"
	if strings.EqualFold(dir, "up") {
		by = "-window.innerHeight*0.8"
	}
	if _, err := e.Page.Eval(cctx, fmt.Sprintf("() => window.scrollBy(0, %s)", by)); err != nil {
		return err
	}
	e.audit(ctx, "action", map[string]any{"kind": "scroll", "dir": dir})
	return nil
}

// Back navigates back in history.
func (e *Executor) Back(ctx context.Context) error {
	cctx, cancel := e.bound(ctx)
	defer cancel()
	if err := e.Page.Back(cctx); err != nil {
		return err
	}
	e.audit(ctx, "action", map[string]any{"kind": "back"})
	return nil
}

// --- helpers (pure, unit-tested without a browser) ------------------------

func nodeLabel(n *perceive.Node) string {
	if n.Name != "" {
		return n.Name
	}
	return n.Text
}

// category names the deny class reported to the model. Account-safety is the
// only guardrail the crawl enforces, so every block is this category.
func category(reason string) string { return "account_safety" }

// substitute replaces {{account.*}} / {{totp}} placeholders with real values.
// It returns whether any secret placeholder was used so the caller can skip the
// foreign-value flag and the audit can avoid logging a resolved secret.
func (e *Executor) substitute(text string) (string, bool, error) {
	used := false
	out := text
	repl := func(ph, val string) {
		if strings.Contains(out, ph) {
			out = strings.ReplaceAll(out, ph, val)
			used = true
		}
	}
	repl("{{account.email}}", e.Secrets.Email)
	repl("{{account.password}}", e.Secrets.Password)
	repl("{{account.phone}}", e.Secrets.Phone)
	if strings.Contains(out, "{{totp}}") {
		code := ""
		if e.Secrets.TOTP != nil {
			c, err := e.Secrets.TOTP()
			if err != nil {
				return "", false, err
			}
			code = c
		}
		out = strings.ReplaceAll(out, "{{totp}}", code)
		used = true
	}
	return out, used, nil
}

func (e *Executor) isOwnIdentity(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return true // empty is not a foreign identity
	}
	return v == strings.ToLower(e.Secrets.Email) || v == strings.ToLower(e.Secrets.Phone)
}

// isIdentityField reports whether a node is an email or phone input (where a
// model-invented value is worth flagging).
func isIdentityField(n *perceive.Node) bool {
	if n.Tag != "input" {
		return false
	}
	if n.Type == "email" || n.Type == "tel" {
		return true
	}
	hay := strings.ToLower(n.NameAttr + " " + n.ID + " " + n.Name + " " + n.Placeholder + " " + n.Autocomplete)
	return strings.Contains(hay, "email") || strings.Contains(hay, "phone") || strings.Contains(hay, "tel")
}

// auditText returns a safe representation of typed text for the audit log: the
// caller's text verbatim unless it resolved a secret placeholder, in which case
// the placeholder shape is kept (the resolved value is never logged).
func auditText(written string, usedPlaceholder bool) string {
	if usedPlaceholder {
		return written // placeholders like {{account.password}} are safe to log
	}
	if len(written) > 80 {
		return written[:80] + "…"
	}
	return written
}
