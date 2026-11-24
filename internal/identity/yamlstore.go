package identity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"gopkg.in/yaml.v3"
)

// YAMLStore is a CredentialStore backed by ONE plaintext YAML file the operator
// edits directly (default <workspace>/accounts.yaml, gitignored). It replaces the
// encrypted per-account FileStore: there is no key. String values may be literals
// or reference an environment variable as ${NAME} (resolved on read only, so a
// Put round-trip keeps the ${NAME} reference in the file rather than baking in a
// resolved secret).
//
//	accounts:
//	  bambulab:
//	    target:    https://bambulab.com
//	    username:  user@example.com
//	    password:  ${BAMBU_PASSWORD}   # literal OR ${ENV}
//	    totp_seed: ""
//	    phone:     ""
//	    profile:   { name: "" }
type YAMLStore struct {
	path string
	mu   sync.Mutex
}

type yamlFile struct {
	Accounts map[string]yamlAccount `yaml:"accounts"`
}

type yamlAccount struct {
	Target   string            `yaml:"target"`
	Username string            `yaml:"username"`
	Password string            `yaml:"password"`
	TOTPSeed string            `yaml:"totp_seed,omitempty"`
	Phone    string            `yaml:"phone,omitempty"`
	Profile  map[string]string `yaml:"profile,omitempty"`
}

// NewYAMLStore returns a store over the YAML file at path, creating its parent
// directory if needed. The file itself is created lazily on first write.
func NewYAMLStore(path string) (*YAMLStore, error) {
	if path == "" {
		return nil, fmt.Errorf("identity: empty credentials file path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("identity: mkdir: %w", err)
	}
	return &YAMLStore{path: path}, nil
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv resolves ${NAME} references against the environment. An unset variable
// expands to empty and logs a warning (so a typo is visible rather than silent).
// A plain value with no ${...} is returned unchanged.
func expandEnv(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(ref string) string {
		name := envRef.FindStringSubmatch(ref)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			fmt.Fprintf(os.Stderr, "identity: warning: %s referenced in credentials but not set\n", name)
			return ""
		}
		return v
	})
}

func (a yamlAccount) credentials() Credentials {
	return Credentials{
		UsernameOrEmail: expandEnv(a.Username),
		Password:        expandEnv(a.Password),
		TOTPSeed:        expandEnv(a.TOTPSeed),
		Phone:           expandEnv(a.Phone),
	}
}

func (a yamlAccount) profile() Profile {
	if len(a.Profile) == 0 {
		return Profile{}
	}
	fields := make(map[string]string, len(a.Profile))
	for k, v := range a.Profile {
		fields[k] = expandEnv(v)
	}
	return Profile{Fields: fields}
}

// load reads the RAW file (no env expansion) so writers can round-trip it.
func (s *YAMLStore) load() (yamlFile, error) {
	var f yamlFile
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return yamlFile{Accounts: map[string]yamlAccount{}}, nil
		}
		return f, fmt.Errorf("identity: read %s: %w", s.path, err)
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("identity: parse %s: %w", s.path, err)
	}
	if f.Accounts == nil {
		f.Accounts = map[string]yamlAccount{}
	}
	return f, nil
}

func (s *YAMLStore) save(f yamlFile) error {
	out, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("identity: encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("identity: mkdir: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("identity: write: %w", err)
	}
	return os.Rename(tmp, s.path)
}

// PutCredentials implements CredentialStore.
func (s *YAMLStore) PutCredentials(_ context.Context, target string, account AccountID, creds Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	a := f.Accounts[string(account)] // preserve any existing profile
	a.Target = target
	a.Username = creds.UsernameOrEmail
	a.Password = creds.Password
	a.TOTPSeed = creds.TOTPSeed
	a.Phone = creds.Phone
	f.Accounts[string(account)] = a
	return s.save(f)
}

// GetCredentials implements CredentialStore.
func (s *YAMLStore) GetCredentials(_ context.Context, account AccountID) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return Credentials{}, err
	}
	a, ok := f.Accounts[string(account)]
	if !ok {
		return Credentials{}, ErrNotFound
	}
	return a.credentials(), nil
}

// PutProfile implements CredentialStore.
func (s *YAMLStore) PutProfile(_ context.Context, account AccountID, p Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	a, ok := f.Accounts[string(account)]
	if !ok {
		return ErrNotFound
	}
	a.Profile = p.Fields
	f.Accounts[string(account)] = a
	return s.save(f)
}

// GetProfile implements CredentialStore.
func (s *YAMLStore) GetProfile(_ context.Context, account AccountID) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return Profile{}, err
	}
	a, ok := f.Accounts[string(account)]
	if !ok {
		return Profile{}, nil
	}
	return a.profile(), nil
}

// Target implements CredentialStore.
func (s *YAMLStore) Target(_ context.Context, account AccountID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return "", err
	}
	a, ok := f.Accounts[string(account)]
	if !ok {
		return "", ErrNotFound
	}
	return expandEnv(a.Target), nil
}

// List implements CredentialStore. An empty target lists all accounts; secrets are
// never included.
func (s *YAMLStore) List(_ context.Context, target string) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []Account
	for id, a := range f.Accounts {
		t := expandEnv(a.Target)
		if target != "" && t != target {
			continue
		}
		out = append(out, Account{
			ID:      AccountID(id),
			Target:  t,
			Creds:   Credentials{UsernameOrEmail: expandEnv(a.Username)}, // no secrets
			Profile: a.profile(),
		})
	}
	return out, nil
}

// compile-time check that YAMLStore satisfies the interface.
var _ CredentialStore = (*YAMLStore)(nil)
