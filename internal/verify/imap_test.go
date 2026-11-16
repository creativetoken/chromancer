package verify

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

// imapFixture starts an in-memory IMAP server and returns a provider wired to it.
func imapFixture(t *testing.T) (*IMAPProvider, *memory.User, func()) {
	t.Helper()
	be := memory.New()
	u, err := be.Login(nil, "username", "password")
	if err != nil {
		t.Fatal(err)
	}
	user := u.(*memory.User)
	if err := user.CreateMailbox("[Gmail]/Spam"); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(be)
	srv.AllowInsecureAuth = true
	go srv.Serve(ln)
	addr := ln.Addr().String()

	p := &IMAPProvider{
		Claims:     NewClaimStore(),
		Poll:       15 * time.Millisecond,
		TargetHint: func(string) string { return "example.com" },
		Resolve: func(account string) (string, MailboxDef, bool) {
			alias := map[string]string{"acct-a": "alias-one@gmail.com", "acct-b": "alias-two@gmail.com"}[account]
			if alias == "" {
				return "", MailboxDef{}, false
			}
			return alias, MailboxDef{ID: "gmail-main", Host: "127.0.0.1", User: "username", Pass: "password"}, true
		},
		Dial: func(mb MailboxDef) (*client.Client, error) {
			c, err := client.Dial(addr)
			if err != nil {
				return nil, err
			}
			if err := c.Login(mb.User, mb.Pass); err != nil {
				return nil, err
			}
			return c, nil
		},
	}
	return p, user, func() { ln.Close() }
}

func appendMsg(t *testing.T, user *memory.User, folder, to, subject, contentType, body string) {
	t.Helper()
	mb, err := user.GetMailbox(folder)
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("From: Acme <no-reply@acme.com>\r\nTo: %s\r\nDelivered-To: %s\r\nSubject: %s\r\nDate: %s\r\nContent-Type: %s\r\n\r\n%s\r\n",
		to, to, subject, time.Now().Format(time.RFC1123Z), contentType, body)
	if err := mb.CreateMessage([]string{}, time.Now(), bytes.NewReader([]byte(raw))); err != nil {
		t.Fatal(err)
	}
}

// appendRaw appends a fully-formed raw message (for multipart bodies).
func appendRaw(t *testing.T, user *memory.User, folder string, at time.Time, raw string) {
	t.Helper()
	mb, err := user.GetMailbox(folder)
	if err != nil {
		t.Fatal(err)
	}
	if err := mb.CreateMessage([]string{}, at, bytes.NewReader([]byte(raw))); err != nil {
		t.Fatal(err)
	}
}

func getEmail(t *testing.T, p *IMAPProvider, account string, wantLink bool, d time.Duration) (Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return p.GetCode(ctx, Request{Channel: Email, Account: account, WantLink: wantLink, Deadline: time.Now().Add(d)})
}

func TestIMAPAliasSeparationAndChannels(t *testing.T) {
	p, user, stop := imapFixture(t)
	defer stop()
	// alias-one gets a code in INBOX; alias-two gets a magic link in Spam.
	appendMsg(t, user, "INBOX", "alias-one@gmail.com", "Your code", "text/plain", "Your verification code is 482913")
	appendMsg(t, user, "[Gmail]/Spam", "alias-two@gmail.com", "Sign in", "text/html",
		`<p>Confirm: <a href="https://app.example.com/verify?token=zzz999">verify</a></p><a href="https://x.com/unsubscribe">no</a>`)

	var wg sync.WaitGroup
	var ra, rb Result
	var ea, eb error
	wg.Add(2)
	go func() { defer wg.Done(); ra, ea = getEmail(t, p, "acct-a", false, 2*time.Second) }()
	go func() { defer wg.Done(); rb, eb = getEmail(t, p, "acct-b", true, 2*time.Second) }()
	wg.Wait()

	if ea != nil || ra.Kind != ResCode || ra.Code != "482913" {
		t.Fatalf("acct-a = %+v err=%v", ra, ea)
	}
	if eb != nil || rb.Kind != ResLink || rb.Link != "https://app.example.com/verify?token=zzz999" {
		t.Fatalf("acct-b = %+v err=%v", rb, eb)
	}
}

func TestIMAPSinceFiltering(t *testing.T) {
	p, user, stop := imapFixture(t)
	defer stop()
	appendMsg(t, user, "INBOX", "alias-one@gmail.com", "old", "text/plain", "code 111111")
	// Since in the future relative to the message: nothing should match.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := p.GetCode(ctx, Request{Channel: Email, Account: "acct-a", Since: time.Now().Add(time.Minute)})
	if err != ErrTimeout {
		t.Fatalf("expected timeout for future Since, got %v", err)
	}
}

// A multipart/alternative message whose plain-text part has no code: the code
// lives only in the HTML part and must still be extracted.
func TestIMAPCodeOnlyInHTMLPart(t *testing.T) {
	p, user, stop := imapFixture(t)
	defer stop()
	raw := "From: Acme <no-reply@acme.com>\r\n" +
		"To: alias-one@gmail.com\r\nDelivered-To: alias-one@gmail.com\r\n" +
		"Subject: Verify Your Identity\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\n" +
		"Sign in to Acme. If you did not request this, ignore this email.\r\n" +
		"--b1\r\nContent-Type: text/html\r\n\r\n" +
		"<html><body><p>Your verification code is <strong>778899</strong></p></body></html>\r\n" +
		"--b1--\r\n"
	appendRaw(t, user, "INBOX", time.Now(), raw)

	r, err := getEmail(t, p, "acct-a", false, 2*time.Second)
	if err != nil || r.Kind != ResCode || r.Code != "778899" {
		t.Fatalf("want code 778899 from HTML part, got %+v err=%v", r, err)
	}
}

// When several code emails are present (a re-sent code invalidates the prior
// one), the newest message's code must be returned, not the oldest.
func TestIMAPReturnsNewestCode(t *testing.T) {
	p, user, stop := imapFixture(t)
	defer stop()
	old := time.Now().Add(-10 * time.Minute)
	recent := time.Now().Add(-30 * time.Second)
	mk := func(at time.Time, code string) string {
		return "From: Acme <no-reply@acme.com>\r\nTo: alias-one@gmail.com\r\n" +
			"Delivered-To: alias-one@gmail.com\r\nSubject: Verify\r\n" +
			"Date: " + at.Format(time.RFC1123Z) + "\r\n\r\n" +
			"Your verification code is " + code + "\r\n"
	}
	// Append oldest last to prove ordering is by date, not arrival/UID order.
	appendRaw(t, user, "INBOX", recent, mk(recent, "222222"))
	appendRaw(t, user, "INBOX", old, mk(old, "111111"))

	r, err := getEmail(t, p, "acct-a", false, 2*time.Second)
	if err != nil || r.Code != "222222" {
		t.Fatalf("want newest code 222222, got %+v err=%v", r, err)
	}
}

func TestIMAPClaimOnce(t *testing.T) {
	p, user, stop := imapFixture(t)
	defer stop()
	appendMsg(t, user, "INBOX", "alias-one@gmail.com", "code", "text/plain", "verification code 909090")
	r, err := getEmail(t, p, "acct-a", false, time.Second)
	if err != nil || r.Code != "909090" {
		t.Fatalf("first = %+v err=%v", r, err)
	}
	// The only message is now claimed: a second wait times out.
	if _, err := getEmail(t, p, "acct-a", false, 200*time.Millisecond); err != ErrTimeout {
		t.Fatalf("second should time out (claimed), got %v", err)
	}
}

func TestIMAPReconnect(t *testing.T) {
	be := memory.New()
	u, _ := be.Login(nil, "username", "password")
	user := u.(*memory.User)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := server.New(be)
	srv.AllowInsecureAuth = true
	go srv.Serve(ln)
	defer ln.Close()
	addr := ln.Addr().String()

	var dials int
	p := &IMAPProvider{
		Claims: NewClaimStore(), Poll: 15 * time.Millisecond,
		Resolve: func(string) (string, MailboxDef, bool) {
			return "alias-one@gmail.com", MailboxDef{ID: "m", Host: "127.0.0.1", User: "username", Pass: "password"}, true
		},
		Dial: func(mb MailboxDef) (*client.Client, error) {
			dials++
			if dials == 1 {
				return nil, fmt.Errorf("simulated dial failure")
			}
			c, err := client.Dial(addr)
			if err != nil {
				return nil, err
			}
			return c, c.Login(mb.User, mb.Pass)
		},
	}
	mb, _ := user.GetMailbox("INBOX")
	raw := "From: a@acme.com\r\nTo: alias-one@gmail.com\r\nDelivered-To: alias-one@gmail.com\r\nSubject: s\r\nDate: " + time.Now().Format(time.RFC1123Z) + "\r\nContent-Type: text/plain\r\n\r\ncode 246810\r\n"
	_ = mb.CreateMessage([]string{}, time.Now(), bytes.NewReader([]byte(raw)))

	r, err := getEmail(t, p, "x", false, 2*time.Second)
	if err != nil || r.Code != "246810" {
		t.Fatalf("after reconnect = %+v err=%v (dials=%d)", r, err, dials)
	}
	if dials < 2 {
		t.Fatalf("expected a reconnect (dials=%d)", dials)
	}
	_ = strings.TrimSpace("")
}
