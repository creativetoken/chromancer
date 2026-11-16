package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/model"
)

// entryScriptModel drives the iterative LLM entry-finder: given the compact page
// in the prompt, it picks the "Create account" link if present, else the
// "Account" menu, else nothing. Reaching the hidden signup form therefore needs
// TWO clicks (Account → Create account) — a one-shot picker would fail.
type entryScriptModel struct{ calls int }

var reEntryAccount = regexp.MustCompile(`#(\d+) a[^\n]*name="Account"`)
var reEntryCreate = regexp.MustCompile(`#(\d+) a[^\n]*name="Create account"`)

func (m *entryScriptModel) Name() string                     { return "entry-script" }
func (m *entryScriptModel) Capabilities() model.Capabilities { return model.Capabilities{} }
func (m *entryScriptModel) Chat(context.Context, []model.Message, model.ChatOpts) (*model.Completion, error) {
	return &model.Completion{Text: "{}"}, nil
}
func (m *entryScriptModel) Structured(_ context.Context, msgs []model.Message, _ json.RawMessage, _ model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	m.calls++
	prompt := ""
	for _, mm := range msgs {
		prompt += mm.Text
	}
	h := 0
	if mm := reEntryCreate.FindStringSubmatch(prompt); mm != nil {
		h = atoi(mm[1])
	} else if mm := reEntryAccount.FindStringSubmatch(prompt); mm != nil {
		h = atoi(mm[1])
	}
	return json.RawMessage([]byte(`{"handle":` + itoa(h) + `,"reason":"progress toward signup"}`)), &model.Usage{}, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestE2EEntryIterativeSignup: the signup form is hidden behind an account menu
// at a non-guessable path, so link/path probes can't find it. The iterative LLM
// entry-finder navigates Account → Create account → form, and signup completes.
func TestE2EEntryIterativeSignup(t *testing.T) {
	skipUnlessE2E(t)
	h := newHarness(t)
	e := h.expect("menu-signup")

	ws := filepath.Join(h.ws, "entryws")
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
	mdl := &entryScriptModel{}
	a.AuthModel = mdl

	if err := a.Creds.PutCredentials(context.Background(), e.Root, "newuser",
		identity.Credentials{UsernameOrEmail: e.Username, Password: e.Password}); err != nil {
		t.Fatal(err)
	}

	// No SignupURL hint: discovery must find the entry on its own.
	r := a.RunAuth(context.Background(), "newuser", auth.Signup, auth.Hints{}, nil)
	if r.Err != nil {
		t.Fatalf("iterative-discovery signup failed: %v (reason=%s)", r.Err, r.Reason)
	}
	// The model had to be consulted at least twice (Account, then Create account).
	if mdl.calls < 2 {
		t.Fatalf("expected the iterative finder to consult the model >=2 times, got %d", mdl.calls)
	}
}
