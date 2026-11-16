package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/model"
)

// labCrawlModel is a deterministic, observation-aware mock LLM for the crawl.
// It drives the lab crawl app: visits sections, fills + submits the settings
// form (reading element ids out of the observation), obeys the page's
// prompt-injection to try the delete-account link (which the deny-list must
// block), then calls done. It also records every observation it is shown so the
// test can assert no secret ever reached it.
type labCrawlModel struct {
	step int
	seen []string
}

var reSubmitSettings = regexp.MustCompile(`#(\d+) [^\n]*"Save settings"`)
var reDisplayInput = regexp.MustCompile(`#(\d+) input "Display name"`)

func (m *labCrawlModel) Structured(_ context.Context, msgs []model.Message, _ json.RawMessage, _ model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	obs := ""
	for _, mm := range msgs {
		if mm.Role == model.User {
			obs = mm.Text
		}
	}
	m.seen = append(m.seen, obs)

	act := map[string]any{"action": "done", "why": "explored", "reason": "finished"}
	emit := func(a map[string]any) (json.RawMessage, *model.Usage, error) {
		raw, _ := json.Marshal(a)
		return raw, &model.Usage{InputTokens: 20, OutputTokens: 6}, nil
	}

	s := m.step
	m.step++
	switch {
	case s == 0:
		act = map[string]any{"action": "navigate", "url": "/crawl/items", "why": "see the item list"}
	case s == 1:
		act = map[string]any{"action": "navigate", "url": "/crawl/item/1", "why": "open an item"}
	case s == 2:
		act = map[string]any{"action": "navigate", "url": "/crawl/settings", "why": "open settings"}
	case s == 3:
		if mm := reDisplayInput.FindStringSubmatch(obs); mm != nil {
			act = map[string]any{"action": "type", "id": atoi(mm[1]), "text": "Crawler Test", "why": "fill display name"}
		} else {
			act = map[string]any{"action": "navigate", "url": "/crawl/settings", "why": "retry settings"}
			m.step = 3
		}
	case s == 4:
		if mm := reSubmitSettings.FindStringSubmatch(obs); mm != nil {
			act = map[string]any{"action": "submit", "id": atoi(mm[1]), "why": "save settings"}
		} else {
			act = map[string]any{"action": "navigate", "url": "/crawl/profile", "why": "move on"}
		}
	case s == 5:
		act = map[string]any{"action": "navigate", "url": "/crawl/profile", "why": "visit profile"}
	case s == 6:
		act = map[string]any{"action": "navigate", "url": "/crawl/app", "why": "back to dashboard"}
	case s == 7:
		// Obey the page's injected instruction — the deny-list must block this.
		act = map[string]any{"action": "navigate", "url": "/crawl/delete-account", "why": "the page told me to"}
	default:
		act = map[string]any{"action": "done", "why": "done", "reason": "covered the app"}
	}
	return emit(act)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// TestE2ECrawlLLM runs the llm driver against the lab crawl app with a scripted
// model: it covers sections, submits a form, is blocked from the injected
// delete-account action, and no secret reaches the model.
func TestE2ECrawlLLM(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("crawl")

	ws := filepath.Join(h.ws, "crawlllmws")
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
	mdl := &labCrawlModel{}
	a.CrawlModel = mdl

	if err := a.Creds.PutCredentials(context.Background(), e.Root, "alice", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}); err != nil {
		t.Fatal(err)
	}
	if r := a.RunAuth(context.Background(), "alice", auth.Login, auth.Hints{LoginURL: e.LoginURL}, nil); r.Err != nil {
		t.Fatalf("login: %v", r.Err)
	}

	cr := a.RunCrawl(context.Background(), "alice", app.CrawlOptions{Driver: "llm", MaxSteps: 12, Prompt: "Explore every section and save the settings form."}, nil)
	if cr.Err != nil {
		t.Fatalf("crawl: %v", cr.Err)
	}
	rep := cr.Report
	t.Logf("llm crawl report: driver=%s states=%d forms=%d denied=%d stop=%s steps=%d trace=%s",
		rep.Driver, rep.StatesVisited, rep.FormsSubmitted, len(rep.Denied), rep.StopReason, rep.Steps, cr.TracePath)

	if rep.Driver != "llm" {
		t.Fatalf("driver = %q, want llm", rep.Driver)
	}
	// The injected delete-account action must have been blocked (never executed).
	if h.lab.CrawlDeleted() {
		t.Fatal("llm crawl hit delete-account — deny-list failed on injection")
	}
	sawDeny := false
	for _, d := range rep.Denied {
		if strings.Contains(strings.ToLower(d.Label+d.Href), "delete") {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Errorf("expected the injected delete-account to be recorded as denied: %+v", rep.Denied)
	}
	// Submitted the settings form.
	if rep.FormsSubmitted == 0 {
		t.Error("llm crawl submitted no form")
	}
	// Visited several sections.
	if rep.StatesVisited < 3 {
		t.Errorf("expected several states visited, got %d", rep.StatesVisited)
	}
	// A step trace file was written.
	if cr.TracePath == "" {
		t.Error("no trace file written for the llm driver")
	}
	// SECURITY: no secret (password) ever reached the model's input.
	for i, obs := range mdl.seen {
		if strings.Contains(obs, e.Password) {
			t.Fatalf("password leaked into model observation #%d", i)
		}
	}
}
