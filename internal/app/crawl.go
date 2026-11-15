package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/explore"
	"github.com/creativetoken/chromancer/internal/handback"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// CrawlResult is returned by RunCrawl.
type CrawlResult struct {
	Report *explore.Report `json:"report"`
	Err    error           `json:"-"`
	Reason string          `json:"reason,omitempty"`
}

// RunCrawl starts from a saved session and crawls the authenticated app like a
// normal user, re-logging-in (on the same browser) if the session drops. It
// never takes an account-safety action.
func (a *App) RunCrawl(ctx context.Context, account string, asker Asker) CrawlResult {
	ws, err := a.Cfg.ForAccount(account)
	if err != nil {
		return CrawlResult{Err: err, Reason: "flow_failed"}
	}
	creds, err := a.Creds.GetCredentials(ctx, identity.AccountID(account))
	if err != nil {
		return CrawlResult{Err: fmt.Errorf("no credentials for %q: %w", account, err), Reason: "flow_failed"}
	}
	target, _ := a.Creds.Target(ctx, identity.AccountID(account))
	sessions := &auth.FileSessionStore{Path: ws.SessionEnc, Key: a.Key}
	saved, err := sessions.Load(ctx)
	if err != nil {
		return CrawlResult{Err: fmt.Errorf("no saved session for %q (run `auth` first): %w", account, err), Reason: "no_session"}
	}

	red := audit.NewRedactor()
	red.AddSecret(creds.Password)
	al, err := audit.OpenFile(ws.AuditPath, red)
	if err != nil {
		return CrawlResult{Err: err, Reason: "flow_failed"}
	}
	defer al.Close()

	bud := core.NewBudget(core.Limits{Time: 4 * time.Minute, Steps: 80})
	bctx, cancel := bud.Context(ctx)
	defer cancel()

	b, err := browser.Launch(bctx, browser.Options{Headless: a.Cfg.Headless, ProfileDir: ws.Profile, ProxyURL: a.Cfg.ProxyURL})
	if err != nil {
		return CrawlResult{Err: fmt.Errorf("launch browser: %w", err), Reason: "flow_failed"}
	}
	defer b.Close()

	p, err := b.NewPage(bctx)
	if err != nil {
		return CrawlResult{Err: err, Reason: "flow_failed"}
	}
	defer p.Close()

	landing := saved.LandingURL
	if landing == "" {
		landing = strings.TrimRight(target, "/") + "/"
	}
	if err := p.ImportState(bctx, saved.State); err != nil {
		return CrawlResult{Err: fmt.Errorf("import session: %w", err), Reason: "flow_failed"}
	}
	if err := p.Navigate(bctx, landing); err != nil {
		return CrawlResult{Err: err, Reason: "flow_failed"}
	}

	// Authenticator reused for mid-crawl re-login on the SAME browser.
	resolver, handoff := a.captchaKit(asker, al, bud, target)
	authr := &auth.Authenticator{
		Target: target, Account: account, Creds: creds,
		Perceiver: perceive.New(), Router: a.Router,
		Sessions: sessions,
		Writer:   &handback.FileWriter{Dir: ws.Artefacts, Account: account},
		Audit:    al, Budget: bud, Verifier: a.verifier(account, creds, asker),
		Captcha: resolver, Handoff: handoff,
		Capture: handback.CaptureWindow{N: 2, T: 4 * time.Second}, EntryCache: ws.CacheDir, Timeout: 8 * time.Second,
	}

	ex := &explore.Explorer{
		Page: p, Perceiver: perceive.New(), Budget: bud, Audit: al,
		IsAuthed:    authedCheck,
		SubmitForms: true,
		Relogin: func(rctx context.Context) error {
			if _, err := authr.Run(rctx, b, auth.Login, auth.Hints{}); err != nil {
				return err
			}
			return nil
		},
	}
	rep, err := ex.Crawl(bctx)
	if err != nil {
		return CrawlResult{Err: err, Reason: "flow_failed"}
	}
	al0, _ := al.Append(ctx, audit.Entry{Actor: "explorer", Kind: "crawl_done", Payload: map[string]any{
		"states": rep.StatesVisited, "templates": rep.URLTemplates, "forms": rep.FormsSubmitted,
		"denied": len(rep.Denied), "relogins": rep.Relogins, "stop": rep.StopReason,
	}})
	_ = al0
	return CrawlResult{Report: rep}
}

// authedCheck is a lightweight "still logged in?" test for the crawl: not on an
// auth route and no visible password field.
func authedCheck(ctx context.Context, p *browser.Page, st perceive.State) bool {
	if auth.IsSSOOnly(st) {
		return false
	}
	f := auth.Classify(st)
	if f.Password != nil {
		return false
	}
	u := strings.ToLower(st.URL)
	if strings.Contains(u, "/login") || strings.Contains(u, "/signin") || strings.Contains(u, "/sign-in") {
		return false
	}
	return true
}
