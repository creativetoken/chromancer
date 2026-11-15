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

// Phone is one SMS number.
type Phone struct {
	ID       string `yaml:"id"`
	Number   string `yaml:"number"` // E.164
	Provider string `yaml:"provider"`
}

// Mailbox is one IMAP mailbox and the alias addresses that land in it.
type Mailbox struct {
	ID          string   `yaml:"id"`
	Provider    string   `yaml:"provider"`
	Host        string   `yaml:"host"`
	Port        int      `yaml:"port"`
	UsernameEnv string   `yaml:"username_env"`
	PasswordEnv string   `yaml:"password_env"`
	Addresses   []string `yaml:"addresses"`
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
	Email       string        `yaml:"email"`
	Phone       string        `yaml:"phone"` // phone id
	PasswordEnv string        `yaml:"password_env"`
	TOTP        []AccountTOTP `yaml:"totp"`
}

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
func (r *Resources) MailboxForAddress(addr string) (*Mailbox, bool) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	for i := range r.Mailboxes {
		for _, a := range r.Mailboxes[i].Addresses {
			if strings.ToLower(strings.TrimSpace(a)) == addr {
				return &r.Mailboxes[i], true
			}
		}
	}
	return nil, false
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
	ids := map[string]bool{}
	for _, p := range r.Phones {
		if p.ID == "" || p.Number == "" {
			return fmt.Errorf("resources: phone needs id and number")
		}
		if ids[p.ID] {
			return fmt.Errorf("resources: duplicate phone id %q", p.ID)
		}
		ids[p.ID] = true
	}
	mids := map[string]bool{}
	for _, m := range r.Mailboxes {
		if m.ID == "" || m.Host == "" || len(m.Addresses) == 0 {
			return fmt.Errorf("resources: mailbox %q needs id, host and at least one address", m.ID)
		}
		if mids[m.ID] {
			return fmt.Errorf("resources: duplicate mailbox id %q", m.ID)
		}
		mids[m.ID] = true
	}
	// Each address maps to exactly one mailbox.
	seen := map[string]string{}
	for _, m := range r.Mailboxes {
		for _, a := range m.Addresses {
			a = strings.ToLower(strings.TrimSpace(a))
			if prev, ok := seen[a]; ok {
				return fmt.Errorf("resources: address %q is claimed by mailboxes %q and %q", a, prev, m.ID)
			}
			seen[a] = m.ID
		}
	}
	// Collect the env vars that must be set, based on what accounts actually use.
	need := map[string]string{} // env name -> why
	usesTwilio := false
	for _, acc := range r.Accounts {
		if acc.ID == "" {
			return fmt.Errorf("resources: account needs an id")
		}
		if acc.PasswordEnv != "" {
			need[acc.PasswordEnv] = "account " + acc.ID + " password"
		}
		if acc.Email != "" {
			mb, ok := r.MailboxForAddress(acc.Email)
			if !ok {
				return fmt.Errorf("resources: account %q email %q maps to no mailbox", acc.ID, acc.Email)
			}
			if mb.UsernameEnv != "" {
				need[mb.UsernameEnv] = "mailbox " + mb.ID + " username"
			}
			if mb.PasswordEnv != "" {
				need[mb.PasswordEnv] = "mailbox " + mb.ID + " password"
			}
		}
		if acc.Phone != "" {
			ph, ok := r.PhoneByID(acc.Phone)
			if !ok {
				return fmt.Errorf("resources: account %q references unknown phone %q", acc.ID, acc.Phone)
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
