package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/verify"
)

const wiringResources = `
resources:
  mailboxes:
    - id: gmail-main
      host: imap.gmail.com
      auth: app_password
      username_env: TEST_GMAIL_USER
      password_env: TEST_GMAIL_PASS
      address_envs:
        - TEST_ALIAS_1
        - TEST_ALIAS_2
accounts:
  - id: acct-a
    email_env: TEST_ALIAS_1
`

// A login account that is NOT named in the resources config (e.g. "buzzfeed")
// must still resolve to a mailbox via its login email, so the automatic IMAP
// provider is used instead of falling back to the human prompt.
func TestBuildVerifyChainResolvesByLoginEmail(t *testing.T) {
	t.Setenv("TEST_GMAIL_USER", "bugtestingp1@gmail.com")
	t.Setenv("TEST_GMAIL_PASS", "apppw")
	t.Setenv("TEST_ALIAS_1", "bugtestingp1+1@gmail.com")
	t.Setenv("TEST_ALIAS_2", "bugtestingp1+2@gmail.com")
	t.Setenv("AGENTBROWSER_KEY", mustKey(t))
	t.Setenv("AGENTBROWSER_WORKSPACE", t.TempDir())

	p := filepath.Join(t.TempDir(), "resources.yaml")
	if err := os.WriteFile(p, []byte(wiringResources), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := verify.LoadResources(p)
	if err != nil {
		t.Fatalf("load resources: %v", err)
	}
	a, err := New(config.Default())
	if err != nil {
		t.Fatalf("app: %v", err)
	}

	// A named account resolves (baseline).
	named := a.BuildVerifyChain(res, verify.NewClaimStore(), nil)
	if !named.Supports(verify.Email, "acct-a") {
		t.Fatal("named account acct-a should support email")
	}

	// An unnamed login account resolves via its login email (which is alias 1).
	login := &LoginHint{Account: "buzzfeed", Email: "bugtestingp1+1@gmail.com"}
	chain := a.BuildVerifyChain(res, verify.NewClaimStore(), login)
	if !chain.Supports(verify.Email, "buzzfeed") {
		t.Fatal("login account buzzfeed should support email via its login address")
	}
	// Without the hint it must NOT resolve (proves the hint is what does it).
	noHint := a.BuildVerifyChain(res, verify.NewClaimStore(), nil)
	if noHint.Supports(verify.Email, "buzzfeed") {
		t.Fatal("buzzfeed must not resolve without a login hint")
	}
}

func mustKey(t *testing.T) string {
	t.Helper()
	k, err := identity.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}
