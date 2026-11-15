package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/lab"
)

// TestE2ECrawl logs in, crawls the authenticated app, and checks it reached all
// ground-truth states, never clicked logout/delete-account, survived the forced
// session expiry (re-login), and stopped on plateau/frontier.
func TestE2ECrawl(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("crawl")

	ws := filepath.Join(h.ws, "crawlws")
	key, _ := identity.NewRandomKey()
	t.Setenv(identity.KeyEnv, key)
	t.Setenv("AGENTBROWSER_LLM", "mock")
	cfg := config.Default()
	cfg.Workspace = ws
	cfg.Headless = true
	a, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Creds.PutCredentials(context.Background(), e.Root, "alice", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}); err != nil {
		t.Fatal(err)
	}

	// 1. Authenticate to produce a saved session.
	if r := a.RunAuth(context.Background(), "alice", auth.Login, auth.Hints{LoginURL: e.LoginURL}, nil); r.Err != nil {
		t.Fatalf("login: %v", r.Err)
	}

	// 2. Crawl.
	cr := a.RunCrawl(context.Background(), "alice", nil)
	if cr.Err != nil {
		t.Fatalf("crawl: %v", cr.Err)
	}
	rep := cr.Report
	t.Logf("crawl report: %+v", rep)

	// Reached all ground-truth states.
	got := map[string]bool{}
	for _, tm := range rep.URLTemplates {
		got[tm] = true
	}
	for _, want := range lab.CrawlStates {
		if !got[want] {
			t.Errorf("crawl did not reach state %q (reached %v)", want, rep.URLTemplates)
		}
	}
	// Submitted the settings form.
	if rep.FormsSubmitted == 0 {
		t.Error("crawl did not submit any form")
	}
	// Never clicked logout or delete-account.
	if h.lab.CrawlDeleted() {
		t.Error("crawl hit the delete-account endpoint")
	}
	// The logout/delete controls were seen and refused.
	sawDeny := false
	for _, d := range rep.Denied {
		if strings.Contains(strings.ToLower(d.Label+d.Href), "log out") || strings.Contains(strings.ToLower(d.Label+d.Href), "delete") {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Errorf("expected logout/delete to be recorded as denied: %+v", rep.Denied)
	}
	// Survived the forced session expiry via re-login.
	if rep.Relogins == 0 {
		t.Error("expected at least one mid-crawl re-login (session expires after N requests)")
	}
	// Terminated cleanly.
	if rep.StopReason != "plateau" && rep.StopReason != "frontier_empty" {
		t.Errorf("unexpected stop reason %q", rep.StopReason)
	}
}
