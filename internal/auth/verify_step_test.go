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

// TestDetectVerifyStepCombinedForm covers forms that keep email + password
// visible alongside a revealed one-time-code field (Bambu Lab's sign-in), and
// that the code step is detected from structural signals without English copy.
func TestDetectVerifyStepCombinedForm(t *testing.T) {
	// Bambu-style: account + password + "Verification code" + Log In / Confirm.
	bambu := perceive.State{Title: "Sign in",
		Text: "Verification Code To keep your account secure, please enter the verification code we sent.",
		Nodes: []perceive.Node{
			{Handle: 1, Tag: "input", Type: "text", Name: "Email Address", Placeholder: "Email Address*", Visible: true},
			{Handle: 2, Tag: "input", Type: "password", Name: "Password", Placeholder: "Password*", Visible: true},
			{Handle: 3, Tag: "input", Type: "text", Name: "code", NameAttr: "code", Placeholder: "Verification code*", MaxLength: 6, Visible: true},
			{Handle: 4, Tag: "button", Type: "submit", Text: "Log In", Visible: true},
			{Handle: 5, Tag: "button", Type: "submit", Text: "Confirm", Visible: true},
		}}
	vs := DetectVerifyStep(bambu)
	if !vs.Needed || vs.CodeField == nil || vs.CodeField.Handle != 3 {
		t.Fatalf("combined form: want code field handle 3, got %+v", vs)
	}
	if vs.Submit == nil || vs.Submit.Handle != 5 {
		t.Fatalf("combined form: want Confirm (handle 5) as submit, got %+v", vs.Submit)
	}

	// Language-independent: non-English labels, no recognizable copy, detected via
	// autocomplete=one-time-code on a field below the credentials.
	jp := perceive.State{Title: "ログイン", Text: "サインインを続けるには確認コードを入力してください",
		Nodes: []perceive.Node{
			{Handle: 1, Tag: "input", Type: "text", Name: "メールアドレス", Placeholder: "メールアドレス", Visible: true},
			{Handle: 2, Tag: "input", Type: "password", Name: "パスワード", Visible: true},
			{Handle: 3, Tag: "input", Type: "text", Name: "確認コード", Autocomplete: "one-time-code", Visible: true},
			{Handle: 4, Tag: "button", Type: "submit", Text: "確認", Visible: true},
		}}
	if vs := DetectVerifyStep(jp); !vs.Needed || vs.CodeField == nil || vs.CodeField.Handle != 3 {
		t.Fatalf("non-English (autocomplete) combined form: %+v", vs)
	}

	// Language-independent: no autocomplete, detected via numeric inputmode + a
	// short maxlength on a field below the credentials, with no code copy.
	de := perceive.State{Title: "Anmelden", Text: "Bitte geben Sie den Bestätigungscode ein, um fortzufahren.",
		Nodes: []perceive.Node{
			{Handle: 1, Tag: "input", Type: "text", Name: "E-Mail-Adresse", Placeholder: "E-Mail", Visible: true},
			{Handle: 2, Tag: "input", Type: "password", Name: "Passwort", Visible: true},
			{Handle: 3, Tag: "input", Type: "text", Name: "Bestätigungscode", InputMode: "numeric", MaxLength: 6, Visible: true},
			{Handle: 4, Tag: "button", Type: "submit", Text: "Weiter", Visible: true},
		}}
	if vs := DetectVerifyStep(de); !vs.Needed || vs.CodeField == nil || vs.CodeField.Handle != 3 {
		t.Fatalf("non-English (inputmode+maxlength) combined form: %+v", vs)
	}

	// Regression guard: a signup with a "confirm password" field and
	// "confirm your account" copy must NOT be mistaken for a code step.
	signup := perceive.State{Title: "Sign up", Text: "Create your account. Confirm your account details below.",
		Nodes: []perceive.Node{
			{Handle: 1, Tag: "input", Type: "text", Name: "Email", Placeholder: "Email", Visible: true},
			{Handle: 2, Tag: "input", Type: "password", Name: "Password", Autocomplete: "new-password", Visible: true},
			{Handle: 3, Tag: "input", Type: "password", Name: "confirm_password", NameAttr: "confirm_password", Autocomplete: "new-password", Visible: true},
			{Handle: 4, Tag: "button", Type: "submit", Text: "Sign up", Visible: true},
		}}
	if DetectVerifyStep(signup).Needed {
		t.Fatal("signup with a confirm-password field should not be a verify step")
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
