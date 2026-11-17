package verify

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestIMAPSubAddressSeparation guards per-account message matching for two
// accounts on the SAME relay: user+1@ and user+5@relay.example both canonicalise
// to one inbox (that is how the mailbox is resolved), but each account's OTP must
// be matched by its EXACT address including the +N — otherwise one account would
// pick up the other's code. Messages for the two aliases are interleaved in a
// single inbox.
func TestIMAPSubAddressSeparation(t *testing.T) {
	p, user, stop := imapFixture(t)
	defer stop()
	// Same base address + different sub-address tags → same inbox, distinct aliases.
	const a1 = "tester_+1@relay.example.com"
	const a5 = "tester_+5@relay.example.com"
	p.Resolve = func(account string) (string, MailboxDef, bool) {
		alias := map[string]string{"relay-1": a1, "relay-5": a5}[account]
		if alias == "" {
			return "", MailboxDef{}, false
		}
		return alias, MailboxDef{ID: "gmail-main", Host: "127.0.0.1", User: "username", Pass: "password"}, true
	}

	raw := func(to, code string, at time.Time) string {
		return fmt.Sprintf("From: Example <no-reply@example.com>\r\nTo: %s\r\nDelivered-To: inbox@example.com\r\nSubject: Your verification code\r\nDate: %s\r\nContent-Type: text/plain\r\n\r\nYour verification code is %s\r\n",
			to, at.Format(time.RFC1123Z), code)
	}
	// Interleave the two accounts' mail, oldest → newest, with a second (newer)
	// round so the test also asserts the newest code PER alias is returned (and
	// never the other alias's, even though its code is newer overall).
	base := time.Now().Add(-10 * time.Minute)
	appendRaw(t, user, "INBOX", base.Add(1*time.Minute), raw(a5, "271828", base.Add(1*time.Minute))) // +5 old
	appendRaw(t, user, "INBOX", base.Add(2*time.Minute), raw(a1, "314159", base.Add(2*time.Minute))) // +1 old
	appendRaw(t, user, "INBOX", base.Add(3*time.Minute), raw(a5, "555555", base.Add(3*time.Minute))) // +5 new
	appendRaw(t, user, "INBOX", base.Add(4*time.Minute), raw(a1, "999999", base.Add(4*time.Minute))) // +1 new (newest overall)

	var wg sync.WaitGroup
	var r1, r5 Result
	var e1, e5 error
	wg.Add(2)
	go func() { defer wg.Done(); r1, e1 = getEmail(t, p, "relay-1", false, 2*time.Second) }()
	go func() { defer wg.Done(); r5, e5 = getEmail(t, p, "relay-5", false, 2*time.Second) }()
	wg.Wait()

	if e1 != nil || r1.Kind != ResCode || r1.Code != "999999" {
		t.Fatalf("relay-1 (+1) must get its own newest code 999999, got %+v err=%v", r1, e1)
	}
	if e5 != nil || r5.Kind != ResCode || r5.Code != "555555" {
		t.Fatalf("relay-5 (+5) must get its own newest code 555555, not +1's, got %+v err=%v", r5, e5)
	}
}
