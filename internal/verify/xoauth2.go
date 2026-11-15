package verify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-sasl"
	"golang.org/x/oauth2"
)

// ErrReauthRequired means the stored OAuth refresh token is no longer valid and
// the operator must rerun `agentbrowser mailbox login`.
var ErrReauthRequired = errors.New("oauth refresh token invalid (invalid_grant); rerun `agentbrowser mailbox login`")

// GmailScope is the IMAP scope Gmail requires.
const GmailScope = "https://mail.google.com/"

// XOAUTH2 SASL mechanism (not built into go-sasl). The initial client response
// is: base64("user=" + user + ^Aauth=Bearer " + token + ^A^A").
type xoauth2Client struct {
	username string
	token    string
}

// NewXOAUTH2 returns a SASL client for the XOAUTH2 mechanism.
func NewXOAUTH2(username, accessToken string) sasl.Client {
	return &xoauth2Client{username: username, token: accessToken}
}

// Start implements sasl.Client.
func (c *xoauth2Client) Start() (mech string, ir []byte, err error) {
	return "XOAUTH2", []byte(XOAUTH2Response(c.username, c.token)), nil
}

// Next implements sasl.Client: on a server error challenge, send an empty
// response so the server returns the failure.
func (c *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	return []byte{}, nil
}

// XOAUTH2Response builds the SASL XOAUTH2 initial response (pre-base64).
func XOAUTH2Response(username, accessToken string) string {
	return "user=" + username + "\x01auth=Bearer " + accessToken + "\x01\x01"
}

// OAuthConfig is a Gmail Desktop-app OAuth client.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	TokenURL     string // override for tests; default is Google's
	AuthURL      string // override for tests; default is Google's
}

func (o OAuthConfig) config() *oauth2.Config {
	tokenURL := o.TokenURL
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}
	authURL := o.AuthURL
	if authURL == "" {
		authURL = "https://accounts.google.com/o/oauth2/auth"
	}
	return &oauth2.Config{
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		Scopes:       []string{GmailScope},
		Endpoint:     oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL},
		RedirectURL:  "http://127.0.0.1/",
	}
}

// AccessToken exchanges a refresh token for a current access token, refreshing
// automatically. An invalid_grant becomes ErrReauthRequired.
func (o OAuthConfig) AccessToken(ctx context.Context, refreshToken string) (string, error) {
	ts := o.config().TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	tok, err := ts.Token()
	if err != nil {
		if isInvalidGrant(err) {
			return "", ErrReauthRequired
		}
		return "", fmt.Errorf("oauth: refresh: %w", err)
	}
	return tok.AccessToken, nil
}

func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode == "invalid_grant" {
			return true
		}
		if strings.Contains(string(re.Body), "invalid_grant") {
			return true
		}
	}
	return strings.Contains(err.Error(), "invalid_grant")
}
