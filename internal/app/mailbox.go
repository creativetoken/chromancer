package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/creativetoken/chromancer/internal/identity"
	"github.com/creativetoken/chromancer/internal/verify"
)

// mailboxTokens stores OAuth refresh tokens for IMAP mailboxes, encrypted at
// rest with the workspace key (same secretbox as credentials). Tokens are never
// logged or printed.
type mailboxTokens struct {
	dir string
	key *[32]byte
}

func (a *App) mailboxTokens() *mailboxTokens {
	return &mailboxTokens{dir: filepath.Join(a.Cfg.Workspace, "mailboxes"), key: a.Key}
}

func (t *mailboxTokens) path(id string) string {
	return filepath.Join(t.dir, sanitizeID(id)+".refresh.enc")
}

func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Get returns the stored refresh token for a mailbox, or "" if none.
func (t *mailboxTokens) Get(id string) (string, error) {
	data, err := os.ReadFile(t.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	plain, err := identity.Decrypt(t.key, data)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Set stores a refresh token encrypted.
func (t *mailboxTokens) Set(id, refreshToken string) error {
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return err
	}
	enc, err := identity.Encrypt(t.key, []byte(refreshToken))
	if err != nil {
		return err
	}
	return os.WriteFile(t.path(id), enc, 0o600)
}

// oauthConfigFor builds the OAuth client for a mailbox from its env vars.
func oauthConfigFor(mb verify.Mailbox, redirect string) (*oauth2.Config, error) {
	cid := os.Getenv(mb.OAuthClientIDEnv)
	csec := os.Getenv(mb.OAuthClientSecretEnv)
	if cid == "" || csec == "" {
		return nil, fmt.Errorf("mailbox %q: set %s and %s (the Desktop OAuth client)", mb.ID, mb.OAuthClientIDEnv, mb.OAuthClientSecretEnv)
	}
	return &oauth2.Config{
		ClientID:     cid,
		ClientSecret: csec,
		Scopes:       []string{verify.GmailScope},
		Endpoint:     oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/auth", TokenURL: "https://oauth2.googleapis.com/token"},
		RedirectURL:  redirect,
	}, nil
}

// AccessTokenFunc returns a closure that yields a fresh access token for an
// oauth2 mailbox, using the stored refresh token. It is wired into MailboxDef.
func (a *App) AccessTokenFunc(mb verify.Mailbox) func() (string, error) {
	return func() (string, error) {
		rt, err := a.mailboxTokens().Get(mb.ID)
		if err != nil {
			return "", err
		}
		if rt == "" {
			return "", fmt.Errorf("mailbox %q has no saved login; run `agentbrowser mailbox login --mailbox %s`", mb.ID, mb.ID)
		}
		cfg := verify.OAuthConfig{ClientID: os.Getenv(mb.OAuthClientIDEnv), ClientSecret: os.Getenv(mb.OAuthClientSecretEnv)}
		return cfg.AccessToken(context.Background(), rt)
	}
}

// RunMailboxLogin runs the OAuth installed-app flow (loopback redirect on
// 127.0.0.1, PKCE) for a Gmail mailbox and stores the refresh token encrypted.
// It prints the URL to open and the outcome, never any token.
func (a *App) RunMailboxLogin(ctx context.Context, mb verify.Mailbox) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	redirect := fmt.Sprintf("http://127.0.0.1:%d/", port)
	cfg, err := oauthConfigFor(mb, redirect)
	if err != nil {
		return err
	}

	verifier := oauth2.GenerateVerifier()
	state := randHex(16)
	authURL := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "consent"))

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	srv := &http.Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			resCh <- result{err: fmt.Errorf("authorization denied: %s", e)}
			fmt.Fprint(w, "Authorization failed. You can close this tab.")
			return
		}
		if q.Get("state") != state {
			resCh <- result{err: fmt.Errorf("state mismatch")}
			fmt.Fprint(w, "State mismatch. You can close this tab.")
			return
		}
		resCh <- result{code: q.Get("code")}
		fmt.Fprint(w, "Login complete. You can close this tab and return to the terminal.")
	})
	srv.Handler = mux
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Fprintf(os.Stderr, "Open this URL in your browser to authorize mailbox %q:\n\n%s\n\nWaiting for the redirect...\n", mb.ID, authURL)

	var code string
	select {
	case r := <-resCh:
		if r.err != nil {
			return r.err
		}
		code = r.code
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("mailbox login timed out")
	}

	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}
	if tok.RefreshToken == "" {
		return fmt.Errorf("Google did not return a refresh token; revoke the app's access and try again")
	}
	if err := a.mailboxTokens().Set(mb.ID, tok.RefreshToken); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Saved login for mailbox %q.\n", mb.ID)
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
