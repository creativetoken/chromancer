package auth

import (
	"testing"

	"github.com/creativetoken/chromancer/internal/perceive"
)

func st(nodes ...perceive.Node) perceive.State {
	return perceive.State{URL: "http://h/login", Title: "Log in", Nodes: nodes}
}

func TestClassifyForm(t *testing.T) {
	s := st(
		perceive.Node{Handle: 1, Tag: "input", Type: "email", Name: "Email", Visible: true},
		perceive.Node{Handle: 2, Tag: "input", Type: "password", Name: "Password", Visible: true},
		perceive.Node{Handle: 3, Tag: "button", Type: "submit", Text: "Log in", Visible: true},
	)
	f := Classify(s)
	if f.Identifier == nil || f.Identifier.Handle != 1 {
		t.Fatal("identifier")
	}
	if f.Password == nil || f.Password.Handle != 2 {
		t.Fatal("password")
	}
	if f.Submit == nil || f.Submit.Handle != 3 {
		t.Fatal("submit")
	}
	if ok, v := IsAuthForm(s, Login); !ok || v != "form" {
		t.Fatalf("IsAuthForm %v %s", ok, v)
	}
}

func TestClassifyMultistep(t *testing.T) {
	s := perceive.State{URL: "http://h/login", Title: "Sign in", Headings: []string{"Sign in"},
		Nodes: []perceive.Node{
			{Handle: 1, Tag: "input", Type: "email", Name: "Email address", Visible: true},
			{Handle: 2, Tag: "button", Type: "submit", Text: "Next", Visible: true},
		}}
	f := Classify(s)
	if f.Identifier == nil || f.Password != nil {
		t.Fatal("expected identifier-only")
	}
	if ok, v := IsAuthForm(s, Login); !ok || v != "multistep" {
		t.Fatalf("IsAuthForm multistep: %v %s", ok, v)
	}
	if !(MultiStepHandler{}).CanHandle(s) || (FormHandler{}).CanHandle(s) {
		t.Fatal("handler selection wrong for multistep")
	}
}

func TestSSOOnly(t *testing.T) {
	s := perceive.State{URL: "http://h/login", Title: "Sign in", Text: "Welcome",
		Nodes: []perceive.Node{
			{Handle: 1, Tag: "button", Name: "Continue with Google", Visible: true},
			{Handle: 2, Tag: "button", Name: "Sign in with GitHub", Visible: true},
		}}
	if !IsSSOOnly(s) {
		t.Fatal("expected SSO-only")
	}
	f := Classify(s)
	if len(f.SSOButtons) != 2 || f.Submit != nil {
		t.Fatalf("sso buttons should not be submit: %+v", f)
	}
	// Native + SSO present -> not SSO-only.
	s.Nodes = append(s.Nodes, perceive.Node{Handle: 3, Tag: "input", Type: "password", Visible: true},
		perceive.Node{Handle: 4, Tag: "input", Type: "email", Name: "Email", Visible: true})
	if IsSSOOnly(s) {
		t.Fatal("native login present, not SSO-only")
	}
}

func TestClassifyAlert(t *testing.T) {
	s := perceive.State{URL: "http://h/login", Title: "Log in", Text: "Invalid email or password. Try again.",
		Nodes: []perceive.Node{
			{Handle: 1, Tag: "input", Type: "email", Name: "Email", Visible: true},
			{Handle: 2, Tag: "input", Type: "password", Visible: true},
			{Handle: 3, Tag: "button", Type: "submit", Text: "Log in", Visible: true},
		}}
	f := Classify(s)
	if !f.HasAlert || f.AlertText == "" {
		t.Fatalf("alert not detected: %+v", f)
	}
}

func TestFailReason(t *testing.T) {
	cases := map[error]string{
		ErrSSOOnly: "sso_only", ErrEntryNotFound: "entrypoint_not_found",
		ErrNotAuthenticated: "not_authenticated", ErrFlowFailed: "flow_failed", nil: "",
	}
	for err, want := range cases {
		if got := FailReason(err); got != want {
			t.Errorf("FailReason(%v)=%s want %s", err, got, want)
		}
	}
}
