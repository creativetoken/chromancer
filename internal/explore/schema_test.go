package explore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/creativetoken/chromancer/internal/perceive"
)

// TestBuildSchemaBaseUnchanged: with no takeover callbacks the schema is exactly
// the base crawl schema (no new verbs leak into an ordinary crawl).
func TestBuildSchemaBaseUnchanged(t *testing.T) {
	e := &Explorer{}
	if string(e.buildSchema()) != string(actionSchema) {
		t.Fatalf("base schema changed when not in takeover mode")
	}
	for _, v := range []string{"enter_verification", "check_logged_in", "give_up"} {
		if strings.Contains(string(e.buildSchema()), v) {
			t.Errorf("base schema must not advertise %q", v)
		}
	}
}

// TestBuildSchemaGating: only the takeover verbs whose callback is set are
// advertised; give_up is always present in takeover mode.
func TestBuildSchemaGating(t *testing.T) {
	e := &Explorer{
		CheckLoggedIn: func(context.Context) (bool, []string) { return false, nil },
		// no EnterVerification / SolveCaptcha / ResendVerification / RequestHuman
	}
	s := string(e.buildSchema())
	enum := actionEnum(t, s)
	must := map[string]bool{"check_logged_in": true, "give_up": true}
	mustNot := map[string]bool{"enter_verification": true, "resend_verification": true, "solve_captcha": true, "request_human": true}
	for _, a := range enum {
		if mustNot[a] {
			t.Errorf("action %q must NOT be advertised (callback not set)", a)
		}
		delete(must, a)
	}
	for a := range must {
		t.Errorf("action %q must be advertised", a)
	}

	// All callbacks set → all verbs present.
	full := &Explorer{
		EnterVerification:  func(context.Context, *perceive.Node, string, bool, int, int) (string, error) { return "", nil },
		ResendVerification: func(context.Context, *perceive.Node) error { return nil },
		SolveCaptcha:       func(context.Context) (string, error) { return "", nil },
		CheckLoggedIn:      func(context.Context) (bool, []string) { return false, nil },
		RequestHuman:       func(context.Context, string) error { return nil },
	}
	fs := string(full.buildSchema())
	for _, v := range []string{"enter_verification", "resend_verification", "solve_captcha", "check_logged_in", "request_human", "give_up", "channel", "want"} {
		if !strings.Contains(fs, v) {
			t.Errorf("full takeover schema missing %q", v)
		}
	}
	// Must still be valid JSON.
	var tmp map[string]any
	if err := json.Unmarshal(full.buildSchema(), &tmp); err != nil {
		t.Fatalf("takeover schema is not valid JSON: %v", err)
	}
}

func actionEnum(t *testing.T, schema string) []string {
	t.Helper()
	var m struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &m); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return m.Properties.Action.Enum
}

// TestBuildSchemaAgentTools: the fill/banner tools are advertised only when their
// callback is wired (same gating as the takeover tools).
func TestBuildSchemaAgentTools(t *testing.T) {
	e := &Explorer{
		FillLoginForm: func(context.Context, *perceive.Node) (string, error) { return "", nil },
		DismissBanner: func(context.Context) (string, error) { return "", nil },
		// no FillSignupForm
	}
	enum := actionEnum(t, string(e.buildSchema()))
	got := map[string]bool{}
	for _, a := range enum {
		got[a] = true
	}
	if !got["fill_login_form"] || !got["dismiss_banner"] {
		t.Errorf("wired tools not advertised: %v", enum)
	}
	if got["fill_signup_form"] {
		t.Errorf("fill_signup_form must NOT be advertised (callback not set): %v", enum)
	}
}
