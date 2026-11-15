package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/identity"
)

// SavedSession is the encrypted on-disk session used for restore-without-login.
type SavedSession struct {
	Target      string               `json:"target"`
	LandingURL  string               `json:"landing_url"`
	State       browser.SessionState `json:"state"`
	AuthHeaders map[string]string    `json:"auth_headers,omitempty"`
	SavedAt     time.Time            `json:"saved_at"`
}

// ErrNoSession is returned when no saved session exists.
var ErrNoSession = errors.New("no saved session")

// SessionStore persists the saved session for an account.
type SessionStore interface {
	Save(ctx context.Context, s SavedSession) error
	Load(ctx context.Context) (SavedSession, error)
	Clear(ctx context.Context) error
}

// FileSessionStore keeps one secretbox-encrypted session file.
type FileSessionStore struct {
	Path string
	Key  *[32]byte
}

// Save implements SessionStore.
func (f *FileSessionStore) Save(_ context.Context, s SavedSession) error {
	plain, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("session: encode: %w", err)
	}
	enc, err := identity.Encrypt(f.Key, plain)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return fmt.Errorf("session: mkdir: %w", err)
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, enc, 0o600); err != nil {
		return fmt.Errorf("session: write: %w", err)
	}
	return os.Rename(tmp, f.Path)
}

// Load implements SessionStore.
func (f *FileSessionStore) Load(_ context.Context) (SavedSession, error) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return SavedSession{}, ErrNoSession
		}
		return SavedSession{}, fmt.Errorf("session: read: %w", err)
	}
	plain, err := identity.Decrypt(f.Key, data)
	if err != nil {
		return SavedSession{}, err
	}
	var s SavedSession
	if err := json.Unmarshal(plain, &s); err != nil {
		return SavedSession{}, fmt.Errorf("session: decode: %w", err)
	}
	return s, nil
}

// Clear implements SessionStore.
func (f *FileSessionStore) Clear(_ context.Context) error {
	err := os.Remove(f.Path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
