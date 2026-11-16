package verify

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Resources models the operator's verification resources: SMS phone numbers,
// email mailboxes (one mailbox can receive several alias addresses), the
// provider credentials, and the accounts that reference them. Secrets are held
// only as the NAMES of environment variables; values are read on demand and
// never stored or logged.
type Resources struct {
	Phones    []Phone      `yaml:"-"`
	Mailboxes []Mailbox    `yaml:"-"`
	Twilio    TwilioConfig `yaml:"-"`
	Accounts  []Account    `yaml:"-"`
	TOTP      TOTPGlobal   `yaml:"-"`
}

// Phone is one SMS number. The number may be given literally or by env var.
type Phone struct {
	ID        string `yaml:"id"`
	Number    string `yaml:"number"`     // E.164 (literal)
	NumberEnv string `yaml:"number_env"` // or the env var holding it
	Provider  string `yaml:"provider"`
}

// Resolve returns the effective E.164 number.
func (p Phone) Resolve() string { return envOr(p.Number, p.NumberEnv) }

func envOr(literal, envName string) string {
	if envName != "" {
		if v := os.Getenv(envName); v != "" {
			return v
		}
	}
	return literal
}

// Mailbox is one IMAP mailbox and the alias addresses that land in it.
type Mailbox struct {
	ID                   string   `yaml:"id"`
	Provider             string   `yaml:"provider"`
	Host                 string   `yaml:"host"`
	Port                 int      `yaml:"port"`
	Auth                 string   `yaml:"auth"` // oauth2 | app_password | password
	UsernameEnv          string   `yaml:"username_env"`
	PasswordEnv          string   `yaml:"password_env"`
	OAuthClientIDEnv     string   `yaml:"oauth_client_id_env"`
	OAuthClientSecretEnv string   `yaml:"oauth_client_secret_env"`
	Addresses            []string `yaml:"addresses"`    // literal alias addresses
	AddressEnvs          []string `yaml:"address_envs"` // or env vars holding them
}

// ResolvedAddresses returns the mailbox's alias addresses (literals + env).
func (m Mailbox) ResolvedAddresses() []string {
	out := append([]string(nil), m.Addresses...)
	for _, e := range m.AddressEnvs {
		if v := os.Getenv(e); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Auth modes.
const (
	AuthOAuth2      = "oauth2"
	AuthAppPassword = "app_password"
	AuthPassword    = "password"
)

// IsGmail reports whether this mailbox is on Gmail's IMAP host.
func (m Mailbox) IsGmail() bool { return strings.EqualFold(m.Host, "imap.gmail.com") }

// AuthMode returns the effective auth mode. For Gmail the default is an app
// password (the simplest mode that works once 2-Step Verification is on, and
// the same mode the ai-hackbot mailbox reference uses); oauth2 is available for
// setups where app passwords are disabled. Other hosts default to plain
// password.
func (m Mailbox) AuthMode() string {
	if m.Auth != "" {
		return strings.ToLower(m.Auth)
	}
	if m.IsGmail() {
		return AuthAppPassword
	}
	return AuthPassword
}

// TwilioConfig is the Twilio provider credentials (by env-var name).
type TwilioConfig struct {
	AccountSIDEnv string `yaml:"account_sid_env"`
	AuthTokenEnv  string `yaml:"auth_token_env"`
	PollInterval  string `yaml:"poll_interval"`
}

// AccountTOTP is a per-target TOTP seed env for an account.
type AccountTOTP struct {
	Target    string `yaml:"target"`
	SecretEnv string `yaml:"secret_env"`
}

// Account references the resources it uses.
type Account struct {
	ID          string        `yaml:"id"`
	Email       string        `yaml:"email"`     // literal alias address
	EmailEnv    string        `yaml:"email_env"` // or the env var holding it
	Phone       string        `yaml:"phone"`     // phone id
	PasswordEnv string        `yaml:"password_env"`
	TOTP        []AccountTOTP `yaml:"totp"`
}

// ResolveEmail returns the account's alias address.
func (a Account) ResolveEmail() string { return envOr(a.Email, a.EmailEnv) }

// TOTPGlobal holds module-wide TOTP options.
type TOTPGlobal struct {
	EnrollWhenPrompted bool `yaml:"enroll_when_prompted"`
}

// yamlFile mirrors the on-disk layout.
type yamlFile struct {
	Resources struct {
		Phones    []Phone   `yaml:"phones"`
		Mailboxes []Mailbox `yaml:"mailboxes"`
	} `yaml:"resources"`
	Providers struct {
		Twilio TwilioConfig `yaml:"twilio"`
	} `yaml:"providers"`
	Accounts []Account  `yaml:"accounts"`
	TOTP     TOTPGlobal `yaml:"totp"`
}

// LoadResources reads and validates a resources YAML file.
func LoadResources(path string) (*Resources, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("resources: no config file given; set $AGENTBROWSER_RESOURCES or pass --resources FILE (see configs/resources.example.yaml)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("resources: %w", err)
	}
	var f yamlFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("resources: parse %s: %w", path, err)
	}
	r := &Resources{
		Phones:    f.Resources.Phones,
		Mailboxes: f.Resources.Mailboxes,
		Twilio:    f.Providers.Twilio,
		Accounts:  f.Accounts,
		TOTP:      f.TOTP,
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// PollInterval returns the Twilio poll interval (default 3s).
func (r *Resources) PollInterval() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(r.Twilio.PollInterval)); err == nil && d > 0 {
		return d
	}
	return 3 * time.Second
}

// MailboxForAddress returns the mailbox that receives address (case-insensitive).
// Matching is sub-addressing-aware: a "+tag" alias resolves to the mailbox for
// the same base account even when the exact alias is not pre-listed, so a
// brand-new signup alias (e.g. user+42@host) works. An address matches a mailbox
// when it equals a listed alias, or when its canonical base equals the canonical
// base of a listed alias or of the mailbox account (username_env).
func (r *Resources) MailboxForAddress(addr string) (*Mailbox, bool) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return nil, false
	}
	canon := canonicalAddress(addr)
	for i := range r.Mailboxes {
		m := &r.Mailboxes[i]
		for _, a := range m.ResolvedAddresses() {
			a = strings.ToLower(strings.TrimSpace(a))
			if a == addr || canonicalAddress(a) == canon {
				return m, true
			}
		}
		if user := os.Getenv(m.UsernameEnv); user != "" && canonicalAddress(user) == canon {
			return m, true
		}
	}
	return nil, false
}

// canonicalAddress reduces an address to the form that identifies one inbox:
// lowercased, with the "+tag" sub-address removed (supported by Gmail, Outlook /
// Office 365, Fastmail, iCloud, Proton and most modern providers). Gmail also
// ignores dots in the local part and treats googlemail.com as gmail.com, so that
// provider gets that extra normalisation.
func canonicalAddress(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return addr
	}
	local, domain := addr[:at], addr[at+1:]
	if plus := strings.IndexByte(local, '+'); plus >= 0 {
		local = local[:plus]
	}
	if domain == "gmail.com" || domain == "googlemail.com" {
		local = strings.ReplaceAll(local, ".", "")
		domain = "gmail.com"
	}
	return local + "@" + domain
}

// PhoneByID returns a phone resource by id.
func (r *Resources) PhoneByID(id string) (*Phone, bool) {
	for i := range r.Phones {
		if r.Phones[i].ID == id {
			return &r.Phones[i], true
		}
	}
	return nil, false
}

// AccountByID returns an account by id.
func (r *Resources) AccountByID(id string) (*Account, bool) {
	for i := range r.Accounts {
		if r.Accounts[i].ID == id {
			return &r.Accounts[i], true
		}
	}
	return nil, false
}

// Validate checks the config is internally consistent and that every referenced
// environment variable is set. It never reads or prints a secret value.
func (r *Resources) Validate() error {
	need := map[string]string{} // env name -> why (verified set at the end)
	ids := map[string]bool{}
	for _, p := range r.Phones {
		if p.ID == "" || (p.Number == "" && p.NumberEnv == "") {
			return fmt.Errorf("resources: phone %q needs id and number (or number_env)", p.ID)
		}
		if ids[p.ID] {
			return fmt.Errorf("resources: duplicate phone id %q", p.ID)
		}
		ids[p.ID] = true
	}
	mids := map[string]bool{}
	for _, m := range r.Mailboxes {
		if m.ID == "" || m.Host == "" || len(m.Addresses)+len(m.AddressEnvs) == 0 {
			return fmt.Errorf("resources: mailbox %q needs id, host and at least one address (or address_envs)", m.ID)
		}
		for _, e := range m.AddressEnvs {
			need[e] = "mailbox " + m.ID + " alias address"
		}
		if mids[m.ID] {
			return fmt.Errorf("resources: duplicate mailbox id %q", m.ID)
		}
		mids[m.ID] = true
		mode := m.AuthMode()
		switch mode {
		case AuthOAuth2, AuthAppPassword, AuthPassword:
		default:
			return fmt.Errorf("resources: mailbox %q has unknown auth %q (use oauth2|app_password|password)", m.ID, m.Auth)
		}
		if mode == AuthPassword && m.IsGmail() {
			return fmt.Errorf("resources: mailbox %q: Gmail does not accept a normal password over IMAP (less-secure app access was removed); use auth: app_password (or oauth2)", m.ID)
		}
	}
	// Each resolved address maps to exactly one mailbox (empties skipped).
	seen := map[string]string{}
	for _, m := range r.Mailboxes {
		for _, a := range m.ResolvedAddresses() {
			a = strings.ToLower(strings.TrimSpace(a))
			if a == "" {
				continue
			}
			if prev, ok := seen[a]; ok && prev != m.ID {
				return fmt.Errorf("resources: an address is claimed by mailboxes %q and %q", prev, m.ID)
			}
			seen[a] = m.ID
		}
	}
	usesTwilio := false
	for _, acc := range r.Accounts {
		if acc.ID == "" {
			return fmt.Errorf("resources: account needs an id")
		}
		if acc.PasswordEnv != "" {
			need[acc.PasswordEnv] = "account " + acc.ID + " password"
		}
		if acc.EmailEnv != "" {
			need[acc.EmailEnv] = "account " + acc.ID + " email address"
		}
		if email := acc.ResolveEmail(); email != "" {
			mb, ok := r.MailboxForAddress(email)
			if !ok {
				return fmt.Errorf("resources: account %q email maps to no mailbox", acc.ID)
			}
			if mb.UsernameEnv != "" {
				need[mb.UsernameEnv] = "mailbox " + mb.ID + " username"
			}
			switch mb.AuthMode() {
			case AuthOAuth2:
				if mb.OAuthClientIDEnv == "" || mb.OAuthClientSecretEnv == "" {
					return fmt.Errorf("resources: mailbox %q (oauth2) needs oauth_client_id_env and oauth_client_secret_env", mb.ID)
				}
				need[mb.OAuthClientIDEnv] = "mailbox " + mb.ID + " oauth client id"
				need[mb.OAuthClientSecretEnv] = "mailbox " + mb.ID + " oauth client secret"
				// The refresh token comes from `mailbox login`, not an env var.
			case AuthAppPassword:
				if mb.PasswordEnv == "" {
					return fmt.Errorf("resources: mailbox %q (app_password) needs password_env (the Gmail app password)", mb.ID)
				}
				need[mb.PasswordEnv] = "mailbox " + mb.ID + " app password"
			default: // password
				if mb.PasswordEnv != "" {
					need[mb.PasswordEnv] = "mailbox " + mb.ID + " password"
				}
			}
		}
		if acc.Phone != "" {
			ph, ok := r.PhoneByID(acc.Phone)
			if !ok {
				return fmt.Errorf("resources: account %q references unknown phone %q", acc.ID, acc.Phone)
			}
			if ph.NumberEnv != "" {
				need[ph.NumberEnv] = "phone " + ph.ID + " number"
			}
			if ph.Provider == "twilio" || ph.Provider == "" {
				usesTwilio = true
			}
		}
		for _, tt := range acc.TOTP {
			if tt.Target == "" || tt.SecretEnv == "" {
				return fmt.Errorf("resources: account %q totp entry needs target and secret_env", acc.ID)
			}
			need[tt.SecretEnv] = "account " + acc.ID + " totp seed for " + tt.Target
		}
	}
	if usesTwilio {
		if r.Twilio.AccountSIDEnv == "" || r.Twilio.AuthTokenEnv == "" {
			return fmt.Errorf("resources: an account uses a Twilio phone but providers.twilio has no account_sid_env/auth_token_env")
		}
		need[r.Twilio.AccountSIDEnv] = "twilio account sid"
		need[r.Twilio.AuthTokenEnv] = "twilio auth token"
	}
	var missing []string
	for env, why := range need {
		if os.Getenv(env) == "" {
			missing = append(missing, fmt.Sprintf("%s (%s)", env, why))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("resources: these environment variables must be set: %s", strings.Join(missing, ", "))
	}
	return nil
}
