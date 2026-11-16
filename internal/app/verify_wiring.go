package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/creativetoken/chromancer/internal/verify"
)

// BuildVerifyChain assembles the automatic verification providers from the
// operator's resources: TOTP (per account+target), Twilio SMS and Gmail IMAP.
// Secrets are read from the environment by the names the resources declare.
func (a *App) BuildVerifyChain(res *verify.Resources, claims *verify.ClaimStore) verify.Provider {
	if claims == nil {
		claims = verify.NewClaimStore()
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
			acc, ok := res.AccountByID(account)
			if !ok {
				return "", verify.MailboxDef{}, false
			}
			email := acc.ResolveEmail()
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
