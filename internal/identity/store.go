// Package identity holds operator-provided credentials and optional signup
// profiles per account, encrypted at rest. Credentials are never generated.
package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/nacl/secretbox"
)

// AccountID is the operator's label for an account (e.g. "alice", "admin").
type AccountID string

// Credentials are OPERATOR-PROVIDED and never generated.
type Credentials struct {
	UsernameOrEmail string `json:"username_or_email"`
	Password        string `json:"password"`
	TOTPSeed        string `json:"totp_seed,omitempty"` // base32, optional
	Phone           string `json:"phone,omitempty"`
}

// Profile holds OPTIONAL operator values for incidental signup fields (name,
// company, …). When absent, the signup flow generates sensible values.
type Profile struct {
	Fields map[string]string `json:"fields,omitempty"`
}

// Account is the stored record for one account of one target.
type Account struct {
	Target  string      `json:"target"`
	ID      AccountID   `json:"id"`
	Creds   Credentials `json:"creds"`
	Profile Profile     `json:"profile,omitempty"`
}

// ErrNotFound is returned when an account has no stored credentials.
var ErrNotFound = errors.New("credentials not found")

// CredentialStore holds operator credentials + optional profile per account,
// encrypted at rest (key from an env var).
type CredentialStore interface {
	PutCredentials(ctx context.Context, target string, account AccountID, creds Credentials) error
	GetCredentials(ctx context.Context, account AccountID) (Credentials, error)
	PutProfile(ctx context.Context, account AccountID, p Profile) error
	GetProfile(ctx context.Context, account AccountID) (Profile, error)
	// Target returns the target an account was registered for ("" if unknown).
	Target(ctx context.Context, account AccountID) (string, error)
	List(ctx context.Context, target string) ([]Account, error)
}

// KeyEnv is the environment variable holding the base64 32-byte store key.
const KeyEnv = "AGENTBROWSER_KEY"

// KeyFromEnv reads and decodes the store key from KeyEnv.
func KeyFromEnv() (*[32]byte, error) {
	raw := strings.TrimSpace(os.Getenv(KeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("identity: %s is not set (expect base64 of 32 random bytes)", KeyEnv)
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("identity: %s must be base64 of exactly 32 bytes", KeyEnv)
	}
	var k [32]byte
	copy(k[:], b)
	return &k, nil
}

// NewRandomKey returns a fresh base64 key suitable for KeyEnv.
func NewRandomKey() (string, error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k[:]), nil
}

// FileStore is a CredentialStore backed by one encrypted file per account
// under <dir>/accounts/<account>/creds.enc.
type FileStore struct {
	dir string
	key *[32]byte
	mu  sync.Mutex
}

// NewFileStore creates a store rooted at dir with the given secretbox key.
func NewFileStore(dir string, key *[32]byte) (*FileStore, error) {
	if key == nil {
		return nil, errors.New("identity: nil key")
	}
	if err := os.MkdirAll(filepath.Join(dir, "accounts"), 0o700); err != nil {
		return nil, fmt.Errorf("identity: mkdir: %w", err)
	}
	return &FileStore{dir: dir, key: key}, nil
}

func (s *FileStore) path(a AccountID) string {
	return filepath.Join(s.dir, "accounts", sanitize(string(a)), "creds.enc")
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

func (s *FileStore) load(a AccountID) (*Account, error) {
	data, err := os.ReadFile(s.path(a))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("identity: read: %w", err)
	}
	plain, err := Decrypt(s.key, data)
	if err != nil {
		return nil, err
	}
	var acc Account
	if err := json.Unmarshal(plain, &acc); err != nil {
		return nil, fmt.Errorf("identity: decode: %w", err)
	}
	return &acc, nil
}

func (s *FileStore) save(acc *Account) error {
	plain, err := json.Marshal(acc)
	if err != nil {
		return fmt.Errorf("identity: encode: %w", err)
	}
	enc, err := Encrypt(s.key, plain)
	if err != nil {
		return err
	}
	p := s.path(acc.ID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("identity: mkdir: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, enc, 0o600); err != nil {
		return fmt.Errorf("identity: write: %w", err)
	}
	return os.Rename(tmp, p)
}

// PutCredentials implements CredentialStore.
func (s *FileStore) PutCredentials(_ context.Context, target string, account AccountID, creds Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, err := s.load(account)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if acc == nil {
		acc = &Account{ID: account}
	}
	acc.Target = target
	acc.Creds = creds
	return s.save(acc)
}

// GetCredentials implements CredentialStore.
func (s *FileStore) GetCredentials(_ context.Context, account AccountID) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, err := s.load(account)
	if err != nil {
		return Credentials{}, err
	}
	return acc.Creds, nil
}

// PutProfile implements CredentialStore.
func (s *FileStore) PutProfile(_ context.Context, account AccountID, p Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, err := s.load(account)
	if err != nil {
		return err
	}
	acc.Profile = p
	return s.save(acc)
}

// GetProfile implements CredentialStore.
func (s *FileStore) GetProfile(_ context.Context, account AccountID) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, err := s.load(account)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Profile{}, nil
		}
		return Profile{}, err
	}
	return acc.Profile, nil
}

// Target implements CredentialStore.
func (s *FileStore) Target(_ context.Context, account AccountID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, err := s.load(account)
	if err != nil {
		return "", err
	}
	return acc.Target, nil
}

// List implements CredentialStore. An empty target lists all accounts.
func (s *FileStore) List(_ context.Context, target string) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, "accounts"))
	if err != nil {
		return nil, fmt.Errorf("identity: list: %w", err)
	}
	var out []Account
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		acc, err := s.load(AccountID(e.Name()))
		if err != nil {
			continue
		}
		if target == "" || acc.Target == target {
			acc.Creds.Password = "" // never list secrets
			acc.Creds.TOTPSeed = ""
			out = append(out, *acc)
		}
	}
	return out, nil
}

// Encrypt seals plaintext with secretbox (nonce prefixed).
func Encrypt(key *[32]byte, plain []byte) ([]byte, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("identity: nonce: %w", err)
	}
	out := secretbox.Seal(nonce[:], plain, &nonce, key)
	return out, nil
}

// Decrypt opens a secretbox payload produced by Encrypt.
func Decrypt(key *[32]byte, data []byte) ([]byte, error) {
	if len(data) < 24 {
		return nil, errors.New("identity: ciphertext too short")
	}
	var nonce [24]byte
	copy(nonce[:], data[:24])
	plain, ok := secretbox.Open(nil, data[24:], &nonce, key)
	if !ok {
		return nil, errors.New("identity: decryption failed (wrong key?)")
	}
	return plain, nil
}
