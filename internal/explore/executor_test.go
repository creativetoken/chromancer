package explore

import (
	"strings"
	"testing"

	"github.com/creativetoken/chromancer/internal/perceive"
)

func TestSubstitutePlaceholders(t *testing.T) {
	e := &Executor{Secrets: Secrets{
		Email:    "bob@x.com",
		Password: "s3cret",
		Phone:    "+15555550100",
		TOTP:     func() (string, error) { return "123456", nil },
	}}
	cases := []struct {
		in, want string
		used     bool
	}{
		{"{{account.email}}", "bob@x.com", true},
		{"{{account.password}}", "s3cret", true},
		{"{{account.phone}}", "+15555550100", true},
		{"{{totp}}", "123456", true},
		{"My Project", "My Project", false},
		{"pre {{account.email}} post", "pre bob@x.com post", true},
	}
	for _, c := range cases {
		got, used, err := e.substitute(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want || used != c.used {
			t.Fatalf("substitute(%q) = (%q,%v), want (%q,%v)", c.in, got, used, c.want, c.used)
		}
	}
}

// The resolved secret value must never be what the audit would log; only the
// placeholder shape survives.
func TestAuditTextNeverLeaksSecret(t *testing.T) {
	e := &Executor{Secrets: Secrets{Password: "s3cret"}}
	_, used, _ := e.substitute("{{account.password}}")
	logged := auditText("{{account.password}}", used)
	if strings.Contains(logged, "s3cret") {
		t.Fatalf("audit text leaked the secret: %q", logged)
	}
	if logged != "{{account.password}}" {
		t.Fatalf("placeholder shape not preserved: %q", logged)
	}
}

func TestIsIdentityFieldAndForeignValue(t *testing.T) {
	e := &Executor{Secrets: Secrets{Email: "bob@x.com", Phone: "+15555550100"}}
	email := &perceive.Node{Tag: "input", Type: "email", Name: "Email"}
	name := &perceive.Node{Tag: "input", Type: "text", Name: "Project name"}
	if !isIdentityField(email) {
		t.Fatal("email input should be an identity field")
	}
	if isIdentityField(name) {
		t.Fatal("a project-name input is not an identity field")
	}
	if e.isOwnIdentity("someone@else.com") {
		t.Fatal("a different email must be foreign")
	}
	if !e.isOwnIdentity("BOB@X.com") {
		t.Fatal("the account's own email (any case) is not foreign")
	}
}

func TestBlockedError(t *testing.T) {
	b := &Blocked{Category: "account_safety"}
	if b.Error() != "blocked: account_safety" {
		t.Fatalf("got %q", b.Error())
	}
	if category("label:Log out") != "account_safety" || category("href:/logout") != "account_safety" {
		t.Fatal("deny reasons must map to account_safety")
	}
}
