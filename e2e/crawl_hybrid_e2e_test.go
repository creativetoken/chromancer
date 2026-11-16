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

// labFormModel only fills forms; in the hybrid driver the LLM is invoked solely
// for form states, so this is all it needs to do.
type labFormModel struct{ calls int }

var reDisplay2 = regexp.MustCompile(`#(\d+) input "Display name"`)
var reSave2 = regexp.MustCompile(`#(\d+) [^\n]*"Save settings"`)

func (m *labFormModel) Structured(_ context.Context, msgs []model.Message, _ json.RawMessage, _ model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	m.calls++
	obs := ""
	for _, mm := range msgs {
		if mm.Role == model.User {
			obs = mm.Text
		}
	}
	var act map[string]any
	switch {
	case reDisplay2.MatchString(obs) && !strings.Contains(obs, "value=Crawler"):
		id := reDisplay2.FindStringSubmatch(obs)[1]
		act = map[string]any{"action": "type", "id": atoi(id), "text": "Crawler Test", "why": "fill display name"}
	case reSave2.MatchString(obs):
		id := reSave2.FindStringSubmatch(obs)[1]
		act = map[string]any{"action": "submit", "id": atoi(id), "why": "save the form"}
	default:
		act = map[string]any{"action": "done", "why": "nothing to fill", "reason": "form handled"}
	}
	raw, _ := json.Marshal(act)
	return raw, &model.Usage{InputTokens: 15, OutputTokens: 5}, nil
}

// TestE2ECrawlHybrid: BFS drives breadth (all sections, denies logout/delete),
// and the LLM fills + submits the settings form.
func TestE2ECrawlHybrid(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("crawl")

	ws := filepath.Join(h.ws, "crawlhybridws")
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
	a.CrawlModel = &labFormModel{}

	if err := a.Creds.PutCredentials(context.Background(), e.Root, "alice", identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}); err != nil {
		t.Fatal(err)
	}
	if r := a.RunAuth(context.Background(), "alice", auth.Login, auth.Hints{LoginURL: e.LoginURL}, nil); r.Err != nil {
		t.Fatalf("login: %v", r.Err)
	}

	cr := a.RunCrawl(context.Background(), "alice", app.CrawlOptions{Driver: "hybrid"}, nil)
	if cr.Err != nil {
		t.Fatalf("crawl: %v", cr.Err)
	}
	rep := cr.Report
	t.Logf("hybrid report: driver=%s states=%d forms=%d denied=%d stop=%s", rep.Driver, rep.StatesVisited, rep.FormsSubmitted, len(rep.Denied), rep.StopReason)

	if rep.Driver != "hybrid" {
		t.Fatalf("driver = %q, want hybrid", rep.Driver)
	}
	if rep.FormsSubmitted == 0 {
		t.Error("hybrid crawl submitted no form (LLM form-fill)")
	}
	if h.lab.CrawlDeleted() {
		t.Error("hybrid crawl hit delete-account")
	}
	sawDeny := false
	for _, d := range rep.Denied {
		if strings.Contains(strings.ToLower(d.Label+d.Href), "delete") || strings.Contains(strings.ToLower(d.Label+d.Href), "log out") {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Errorf("expected logout/delete denied by BFS breadth: %+v", rep.Denied)
	}
	if rep.StatesVisited < 4 {
		t.Errorf("expected BFS breadth to reach several states, got %d", rep.StatesVisited)
	}
}
