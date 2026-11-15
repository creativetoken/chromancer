package auth

import (
	"testing"

	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/perceive"
	"github.com/creativetoken/chromancer/internal/verify"
)

func identityProfile(fields map[string]string) identity.Profile {
	return identity.Profile{Fields: fields}
}

func cred(email string) identity.Credentials {
	return identity.Credentials{UsernameOrEmail: email, Password: "correct horse"}
}

func TestDetectVerifyStep(t *testing.T) {
	emailOTP := perceive.State{Title: "Verify", Headings: []string{"Verify your email"},
		Text: "We emailed a 6-digit verification code to a***@example.com. Enter it below.",
		Nodes: []perceive.Node{
			{Handle: 1, Tag: "input", Type: "text", Name: "Verification code", Autocomplete: "one-time-code", Visible: true},
			{Handle: 2, Tag: "button", Type: "submit", Text: "Verify", Visible: true},
		}}
	vs := DetectVerifyStep(emailOTP)
	if !vs.Needed || vs.Channel != verify.Email || vs.CodeField == nil || vs.CodeField.Handle != 1 {
		t.Fatalf("email otp: %+v", vs)
	}

	totp := perceive.State{Title: "MFA", Headings: []string{"Two-factor authentication"},
		Text: "Open your authenticator app and enter the 6-digit code.",
		Nodes: []perceive.Node{
			{Handle: 1, Tag: "input", Type: "text", Name: "Authentication code", Autocomplete: "one-time-code", Visible: true},
			{Handle: 2, Tag: "button", Type: "submit", Text: "Verify", Visible: true},
		}}
	if vs := DetectVerifyStep(totp); !vs.Needed || vs.Channel != verify.TOTP {
		t.Fatalf("totp: %+v", vs)
	}

	sms := perceive.State{Title: "Code", Text: "We sent a text message with a code to your phone ending in **0100.",
		Nodes: []perceive.Node{{Handle: 1, Tag: "input", Type: "text", Name: "Code", Autocomplete: "one-time-code", Visible: true}}}
	if vs := DetectVerifyStep(sms); !vs.Needed || vs.Channel != verify.SMS {
		t.Fatalf("sms: %+v", vs)
	}

	magic := perceive.State{Title: "Check your email", Headings: []string{"Check your email"},
		Text:  "We emailed you a magic link to sign in. Click the link in your inbox to continue.",
		Nodes: []perceive.Node{{Handle: 1, Tag: "a", Name: "Home", Href: "/", Visible: true}}}
	vs = DetectVerifyStep(magic)
	if !vs.Needed || !vs.LinkOnly || vs.Channel != verify.Email {
		t.Fatalf("magic link: %+v", vs)
	}

	// A plain login form is NOT a verification step.
	login := st(
		perceive.Node{Handle: 1, Tag: "input", Type: "email", Name: "Email", Visible: true},
		perceive.Node{Handle: 2, Tag: "input", Type: "password", Visible: true},
		perceive.Node{Handle: 3, Tag: "button", Type: "submit", Text: "Log in", Visible: true},
	)
	if DetectVerifyStep(login).Needed {
		t.Fatal("login form should not be a verify step")
	}
}

func TestIsAccountExists(t *testing.T) {
	yes := perceive.State{Text: "An account with that email already exists. Try logging in."}
	if !IsAccountExists(yes) {
		t.Fatal("should detect account exists")
	}
	no := perceive.State{Text: "Welcome, please sign up."}
	if IsAccountExists(no) {
		t.Fatal("false positive")
	}
}

func TestSignupProfileGeneratesAndUsesProfile(t *testing.T) {
	h := SignupHandler{Profile: identityProfile(map[string]string{"company": "Acme Inc"})}
	company := &perceive.Node{Tag: "input", Type: "text", Name: "Company", NameAttr: "company"}
	if got := h.profileValue(company, cred("alice@example.com")); got != "Acme Inc" {
		t.Fatalf("company from profile: %q", got)
	}
	name := &perceive.Node{Tag: "input", Type: "text", Name: "Full name", NameAttr: "name"}
	if got := h.profileValue(name, cred("alice@example.com")); got == "" {
		t.Fatalf("name should be generated, got empty")
	}
	user := &perceive.Node{Tag: "input", Type: "text", Name: "Username", NameAttr: "username"}
	if got := h.profileValue(user, cred("alice@example.com")); got != "alice" {
		t.Fatalf("username local part: %q", got)
	}
}
