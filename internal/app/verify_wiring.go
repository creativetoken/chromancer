package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/creativetoken/chromancer/internal/verify"
)

// LoginHint ties the agentbrowser login account to the email/phone it signs in
// with, so the verify chain can resolve a mailbox for a login account that has
// no explicit entry in the resources config (its login email is matched against
// the mailboxes' alias addresses). A nil hint keeps the resources-only
// behaviour.
type LoginHint struct {
	Account string
	Email   string
}

// BuildVerifyChain assembles the automatic verification providers from the
// operator's resources: TOTP (per account+target), Twilio SMS and Gmail IMAP.
// Secrets are read from the environment by the names the resources declare.
// login, when non-nil, lets the current login account resolve to a mailbox by
// its login email even if it is not a named account in the resources config.
func (a *App) BuildVerifyChain(res *verify.Resources, claims *verify.ClaimStore, login *LoginHint) verify.Provider {
	if claims == nil {
		claims = verify.NewClaimStore()
	}
	// emailFor returns the address to receive verification mail for an account:
	// its resources entry if present, else the current login account's email.
	emailFor := func(account string) string {
		if acc, ok := res.AccountByID(account); ok {
			if e := acc.ResolveEmail(); e != "" {
				return e
			}
		}
		if login != nil && login.Account == account {
			return login.Email
		}
		return ""
	}
	numberFor := func(account string) string {
		acc, ok := res.AccountByID(account)
		if !ok || acc.Phone == "" {
			return ""
		}
		ph, ok := res.PhoneByID(acc.Phone)
		if !ok {
			return ""
		}
		return ph.Resolve()
	}
	twilio := &verify.TwilioProvider{
		AccountSID: os.Getenv(res.Twilio.AccountSIDEnv),
		AuthToken:  os.Getenv(res.Twilio.AuthTokenEnv),
		Poll:       res.PollInterval(),
		Claims:     claims,
		NumberFor:  numberFor,
	}

	imap := &verify.IMAPProvider{
		Claims: claims,
		Resolve: func(account string) (string, verify.MailboxDef, bool) {
			email := emailFor(account)
			if email == "" {
				return "", verify.MailboxDef{}, false
			}
			mb, ok := res.MailboxForAddress(email)
			if !ok {
				return "", verify.MailboxDef{}, false
			}
			def := verify.MailboxDef{ID: mb.ID, Host: mb.Host, Port: mb.Port, Auth: mb.AuthMode(), User: os.Getenv(mb.UsernameEnv)}
			if mb.AuthMode() == verify.AuthOAuth2 {
				def.AccessToken = a.AccessTokenFunc(*mb)
			} else {
				def.Pass = os.Getenv(mb.PasswordEnv)
			}
			return email, def, true
		},
	}

	totp := &verify.TOTPProvider{
		SeedForTarget: func(account, target string) string {
			acc, ok := res.AccountByID(account)
			if !ok {
				return ""
			}
			for _, t := range acc.TOTP {
				if target == "" || t.Target == target || strings.Contains(target, t.Target) {
					return os.Getenv(t.SecretEnv)
				}
			}
			return ""
		},
	}

	// AGENTBROWSER_VERIFY_DEBUG prints a per-folder funnel (counts only, no
	// addresses/subjects/codes) so a timed-out email test shows which stage drops
	// the message: fetched -> aliasMatched -> newer -> extracted.
	if os.Getenv("AGENTBROWSER_VERIFY_DEBUG") != "" {
		imap.OnScan = func(folder string, fetched, aliasMatched, newer, claimed, extracted int) {
			fmt.Fprintf(os.Stderr,
				"[verify] folder=%s fetched=%d aliasMatched=%d newer=%d alreadyClaimed=%d extracted=%d\n",
				folder, fetched, aliasMatched, newer, claimed, extracted)
		}
		imap.OnFetchError = func(folder string, err error) {
			fmt.Fprintf(os.Stderr, "[verify] folder=%s FETCH ERROR: %v\n", folder, err)
		}
	}

	// TOTP first (instant), then the inbox providers.
	return verify.Chain{totp, twilio, imap}
}
