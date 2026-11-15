//go:build live

// Live smoke tests against the operator's real Twilio and Gmail. Run with
// `make live` (which loads .env.live). They are READ-ONLY: they list recent
// messages and run the extractors, and never send, delete, move, label or mark
// anything. They print only pass/fail, counts and timings, never any value,
// number, address, body, code or link.
package verify

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func env(t *testing.T, name string) (string, bool) {
	v := os.Getenv(name)
	if v == "" {
		t.Logf("%s not set, skipping", name)
		return "", false
	}
	return v, true
}

func TestLiveTwilio(t *testing.T) {
	sid, ok1 := env(t, "TWILIO_ACCOUNT_SID")
	tok, ok2 := env(t, "TWILIO_AUTH_TOKEN")
	if !ok1 || !ok2 {
		t.Skip("Twilio credentials not set")
	}
	for i, numEnv := range []string{"TWILIO_NUMBER_1", "TWILIO_NUMBER_2"} {
		num, ok := env(t, numEnv)
		if !ok {
			continue
		}
		p := &TwilioProvider{AccountSID: sid, AuthToken: tok, HTTP: &http.Client{Timeout: 20 * time.Second}}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		start := time.Now()
		msgs, err := p.list(ctx, num)
		cancel()
		if err != nil {
			// A transport/network problem is a skip; a 4xx (bad creds) is a fail.
			if isAuthError(err) {
				t.Fatalf("twilio-%d: authentication failed", i+1)
			}
			t.Skipf("twilio-%d: could not reach Twilio (%v); skipping", i+1, err)
		}
		inbound, found := 0, 0
		for _, m := range msgs {
			if m.Direction == "" || m.Direction == "inbound" {
				inbound++
				if _, ok := ExtractCode(m.Body); ok {
					found++
				}
			}
		}
		t.Logf("twilio-%d: connected, %d recent inbound, extractor found a code in %d/%d (%s)",
			i+1, inbound, found, inbound, time.Since(start).Round(time.Millisecond))
	}
}

func TestLiveGmail(t *testing.T) {
	user, ok1 := env(t, "GMAIL_USER")
	pass, ok2 := env(t, "GMAIL_APP_PASSWORD")
	if !ok1 || !ok2 {
		t.Skip("Gmail credentials not set")
	}
	mb := MailboxDef{ID: "gmail-live", Host: "imap.gmail.com", Port: 993, User: user, Pass: pass}
	for i, aliasEnv := range []string{"GMAIL_ALIAS_1", "GMAIL_ALIAS_2"} {
		alias, ok := env(t, aliasEnv)
		if !ok {
			continue
		}
		p := &IMAPProvider{
			Claims:  NewClaimStore(),
			Folders: []string{"INBOX"}, // read-only; stay out of other folders by default
			Resolve: func(string) (string, MailboxDef, bool) { return alias, mb, true },
		}
		start := time.Now()
		conn := p.conn(mb)
		msgs, err := conn.fetchRecent("INBOX", time.Now().Add(-24*time.Hour))
		if err != nil {
			t.Skipf("gmail-%d: could not reach Gmail (%v); skipping", i+1, err)
		}
		toAlias, found := 0, 0
		for _, m := range msgs {
			if !m.toAlias(alias) {
				continue
			}
			toAlias++
			if _, ok := ExtractCode(m.text); ok {
				found++
			} else if _, ok := ExtractLink(m.text+"\n"+m.html, ""); ok {
				found++
			}
		}
		t.Logf("gmail-%d: connected, %d recent to alias, extractor found a code/link in %d/%d (%s)",
			i+1, toAlias, found, toAlias, time.Since(start).Round(time.Millisecond))
		conn.drop()
	}
}

func isAuthError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "http 401") || strings.Contains(s, "http 403")
}
