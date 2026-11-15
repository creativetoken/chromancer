package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodConfig = `
resources:
  phones:
    - id: twilio-1
      number: "+441234567890"
      provider: twilio
    - id: twilio-2
      number: "+441234567891"
      provider: twilio
  mailboxes:
    - id: gmail-main
      provider: imap
      host: imap.gmail.com
      port: 993
      username_env: GMAIL_USER
      password_env: GMAIL_APP_PASSWORD
      addresses:
        - alias-one@gmail.com
        - alias-two@gmail.com
providers:
  twilio:
    account_sid_env: TWILIO_ACCOUNT_SID
    auth_token_env: TWILIO_AUTH_TOKEN
    poll_interval: 2s
accounts:
  - id: acct-a
    email: alias-one@gmail.com
    phone: twilio-1
    password_env: ACCT_A_PASSWORD
    totp:
      - target: app.example.com
        secret_env: ACCT_A_EXAMPLE_TOTP
  - id: acct-b
    email: alias-two@gmail.com
    phone: twilio-2
    password_env: ACCT_B_PASSWORD
totp:
  enroll_when_prompted: true
`

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "resources.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func setEnvs(t *testing.T) {
	for _, e := range []string{"GMAIL_USER", "GMAIL_APP_PASSWORD", "TWILIO_ACCOUNT_SID", "TWILIO_AUTH_TOKEN", "ACCT_A_PASSWORD", "ACCT_B_PASSWORD", "ACCT_A_EXAMPLE_TOTP"} {
		t.Setenv(e, "x")
	}
}

func TestLoadResourcesGood(t *testing.T) {
	setEnvs(t)
	r, err := LoadResources(writeCfg(t, goodConfig))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(r.Phones) != 2 || len(r.Mailboxes) != 1 || len(r.Accounts) != 2 {
		t.Fatalf("counts: %+v", r)
	}
	if !r.TOTP.EnrollWhenPrompted {
		t.Error("enroll_when_prompted should be true")
	}
	if r.PollInterval().String() != "2s" {
		t.Errorf("poll interval = %s", r.PollInterval())
	}
	mb, ok := r.MailboxForAddress("Alias-Two@Gmail.com")
	if !ok || mb.ID != "gmail-main" {
		t.Errorf("alias resolve: %v %+v", ok, mb)
	}
	acc, _ := r.AccountByID("acct-a")
	if acc.TOTP[0].Target != "app.example.com" {
		t.Errorf("acct-a totp: %+v", acc.TOTP)
	}
}

func TestLoadResourcesBad(t *testing.T) {
	setEnvs(t)
	cases := map[string]string{
		"email maps to no mailbox": strings.Replace(goodConfig, "email: alias-one@gmail.com", "email: nobody@gmail.com", 1),
		"unknown phone":            strings.Replace(goodConfig, "phone: twilio-1", "phone: twilio-9", 1),
		"address in two mailboxes": goodConfig + `
` + "resources:\n", // malformed duplicate block -> parse-ish; replaced below
	}
	// Replace the malformed case with a real duplicate-address config.
	cases["address in two mailboxes"] = strings.Replace(goodConfig,
		"    - id: gmail-main",
		"    - id: gmail-two\n      host: imap.gmail.com\n      addresses: [alias-one@gmail.com]\n    - id: gmail-main", 1)

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadResources(writeCfg(t, cfg)); err == nil {
				t.Fatalf("expected an error for %q", name)
			}
		})
	}
}

func TestLoadResourcesMissingEnv(t *testing.T) {
	// Only set some of the required envs; a missing one must fail fast and name
	// the variable (never its value).
	for _, e := range []string{"GMAIL_USER", "GMAIL_APP_PASSWORD", "TWILIO_ACCOUNT_SID", "TWILIO_AUTH_TOKEN", "ACCT_A_PASSWORD", "ACCT_A_EXAMPLE_TOTP"} {
		t.Setenv(e, "x")
	}
	// ACCT_B_PASSWORD deliberately unset.
	t.Setenv("ACCT_B_PASSWORD", "")
	_, err := LoadResources(writeCfg(t, goodConfig))
	if err == nil || !strings.Contains(err.Error(), "ACCT_B_PASSWORD") {
		t.Fatalf("expected a missing-env error naming ACCT_B_PASSWORD, got %v", err)
	}
}
