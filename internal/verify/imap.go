package verify

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	netmail "net/mail"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	_ "github.com/emersion/go-message/charset" // register non-UTF-8 charset decoders for MIME bodies/headers
	"github.com/emersion/go-message/mail"
)

// MailboxDef is the connection detail for one IMAP mailbox.
type MailboxDef struct {
	ID   string
	Host string
	Port int
	Auth string // oauth2 | app_password | password ("" = password)
	User string
	Pass string // app password or plain password
	// AccessToken returns a fresh OAuth access token (oauth2 mailboxes only).
	AccessToken func() (string, error)
}

func (m MailboxDef) addr() string {
	port := m.Port
	if port == 0 {
		port = 993
	}
	return fmt.Sprintf("%s:%d", m.Host, port)
}

// IMAPProvider reads verification email for an account from a Gmail-style
// mailbox. One mailbox can receive several alias addresses; a single connection
// is shared across the accounts that alias into it, and each account only ever
// receives messages addressed to its own alias. Messages are claimed once.
type IMAPProvider struct {
	// Resolve returns the alias address and mailbox for an account.
	Resolve func(account string) (alias string, mb MailboxDef, ok bool)
	// TargetHint returns the app domain, to prefer links pointing at it.
	TargetHint func(account string) string
	Claims     *ClaimStore
	Poll       time.Duration
	Skew       time.Duration
	Folders    []string // default INBOX + [Gmail]/Spam
	// Dial opens and logs in a client; default is TLS on the mailbox addr.
	Dial func(mb MailboxDef) (*client.Client, error)
	// Ambiguous is an optional LLM fallback given one message's text.
	Ambiguous func(ctx context.Context, subject, from, text string) (kind ResultKind, value string, ok bool)
	// OnScan, if set, reports a per-folder diagnostic funnel each poll pass. It
	// receives only counts (never any address, subject, code or link) so it is
	// safe to log: it shows which stage drops the message.
	OnScan func(folder string, fetched, aliasMatched, newer, claimed, extracted int)
	// OnFetchError, if set, reports a folder fetch/login error (otherwise
	// swallowed so polling can retry). The error is connection/protocol text, not
	// message content.
	OnFetchError func(folder string, err error)

	mu   sync.Mutex
	pool map[string]*mailboxConn
}

// Supports implements Provider: email, when the account resolves to a mailbox.
func (p *IMAPProvider) Supports(ch Channel, account string) bool {
	if ch != Email || p.Resolve == nil {
		return false
	}
	_, _, ok := p.Resolve(account)
	return ok
}

// Resend is a no-op (the flow handler clicks resend).
func (p *IMAPProvider) Resend(context.Context, Request) error { return nil }

func (p *IMAPProvider) conn(mb MailboxDef) *mailboxConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pool == nil {
		p.pool = map[string]*mailboxConn{}
	}
	c, ok := p.pool[mb.ID]
	if !ok {
		dial := p.Dial
		if dial == nil {
			dial = defaultDial
		}
		c = &mailboxConn{mb: mb, dial: dial}
		p.pool[mb.ID] = c
	}
	return c
}

// GetCode polls the mailbox for a message to the account's alias newer than
// Since and returns its code or link.
func (p *IMAPProvider) GetCode(ctx context.Context, req Request) (Result, error) {
	alias, mb, ok := p.Resolve(req.Account)
	if !ok {
		return Result{}, ErrUnsupported
	}
	conn := p.conn(mb)
	poll := p.Poll
	if poll == 0 {
		poll = time.Second
	}
	since := req.Since.Add(-p.skew())
	domain := ""
	if p.TargetHint != nil {
		domain = p.TargetHint(req.Account)
	}
	folders := p.Folders
	if len(folders) == 0 {
		folders = conn.folders() // INBOX + the discovered spam/junk folder
	}
	for {
		// Gather candidates from every folder, then try them newest-first. A
		// re-sent code invalidates the previous one, so the most recent matching
		// message is the only one worth submitting; picking the oldest would type
		// a superseded code and get "code is invalid".
		var (
			cands                    []parsedMsg
			fetched                  int
			nAlias, nNewer, nClaimed int
		)
		for _, folder := range folders {
			msgs, err := conn.fetchRecent(folder, since)
			if err != nil {
				if p.OnFetchError != nil {
					p.OnFetchError(folder, err)
				}
				continue
			}
			fetched += len(msgs)
			for _, m := range msgs {
				if !m.toAlias(alias) {
					continue
				}
				nAlias++
				if !m.date.After(since) {
					continue
				}
				nNewer++
				if p.Claims != nil && p.Claims.Claimed(m.id) {
					nClaimed++
					continue
				}
				cands = append(cands, m)
			}
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].date.After(cands[j].date) })
		for _, m := range cands {
			res, ok := p.extract(ctx, req, m, domain)
			if !ok {
				continue
			}
			if p.OnScan != nil {
				p.OnScan("*", fetched, nAlias, nNewer, nClaimed, 1)
			}
			if p.Claims != nil {
				p.Claims.Claim(m.id)
			}
			return res, nil
		}
		if p.OnScan != nil {
			p.OnScan("*", fetched, nAlias, nNewer, nClaimed, 0)
		}
		select {
		case <-time.After(poll):
		case <-ctx.Done():
			return Result{}, ErrTimeout
		}
	}
}

func (p *IMAPProvider) skew() time.Duration {
	if p.Skew == 0 {
		return 30 * time.Second
	}
	return p.Skew
}

// extract pulls a code or link from a message per the request's preference.
func (p *IMAPProvider) extract(ctx context.Context, req Request, m parsedMsg, domain string) (Result, bool) {
	// The code may live only in the HTML part (the plain-text part can be a stub
	// with no code), so search the plain text first and then the tag-stripped
	// HTML separately (concatenating could duplicate the code and trip the
	// ambiguity guard). The reference mailbox implementation searches both too.
	htmlText := stripTags(m.html)
	linkText := m.text + "\n" + m.html
	if req.WantLink {
		if link, ok := ExtractLink(linkText, domain); ok {
			return Result{Kind: ResLink, Link: link}, true
		}
	}
	if code, ok := ExtractCode(m.text); ok {
		return Result{Kind: ResCode, Code: code}, true
	}
	if strings.TrimSpace(htmlText) != "" && htmlText != m.text {
		if code, ok := ExtractCode(htmlText); ok {
			return Result{Kind: ResCode, Code: code}, true
		}
	}
	if link, ok := ExtractLink(linkText, domain); ok {
		return Result{Kind: ResLink, Link: link}, true
	}
	if p.Ambiguous != nil {
		if kind, val, ok := p.Ambiguous(ctx, m.subject, m.from, m.text); ok {
			if kind == ResLink {
				return Result{Kind: ResLink, Link: val}, true
			}
			return Result{Kind: ResCode, Code: val}, true
		}
	}
	return Result{}, false
}

func defaultDial(mb MailboxDef) (*client.Client, error) {
	c, err := client.DialTLS(mb.addr(), &tls.Config{ServerName: mb.Host})
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(mb.Auth, "oauth2") {
		if mb.AccessToken == nil {
			_ = c.Logout()
			return nil, fmt.Errorf("imap: oauth2 mailbox %q has no token source (run `agentbrowser mailbox login`)", mb.ID)
		}
		tok, err := mb.AccessToken()
		if err != nil {
			_ = c.Logout()
			return nil, err
		}
		if err := c.Authenticate(NewXOAUTH2(mb.User, tok)); err != nil {
			_ = c.Logout()
			return nil, err
		}
		return c, nil
	}
	user := strings.TrimSpace(mb.User)
	pass := mb.Pass
	if strings.EqualFold(mb.Auth, "app_password") {
		// Google shows an app password as "abcd efgh ijkl mnop"; the spaces are
		// display-only and are not part of the secret. Strip all whitespace so a
		// value copied verbatim still works.
		pass = strings.Join(strings.Fields(pass), "")
	}
	if err := c.Login(user, pass); err != nil {
		_ = c.Logout()
		return nil, err
	}
	return c, nil
}

// mailboxConn is one shared, serialized IMAP connection. A long-lived
// connection reused across polls is more reliable against Gmail than dialing
// fresh every time (which produces "unexpected EOF" under churn); liveness is
// checked with a cheap NOOP and a dead connection is transparently redialed.
type mailboxConn struct {
	mu       sync.Mutex
	mb       MailboxDef
	cli      *client.Client
	dial     func(mb MailboxDef) (*client.Client, error)
	junkOnce sync.Once
	cache    []string // INBOX + discovered junk folder
}

// ensure returns a live client, NOOP-checking an existing one and redialing a
// dead one.
func (c *mailboxConn) ensure() error {
	if c.cli != nil {
		if err := c.cli.Noop(); err == nil {
			return nil
		}
		c.drop() // dead connection; redial below
	}
	cli, err := c.dial(c.mb)
	if err != nil {
		return err
	}
	c.cli = cli
	return nil
}

func (c *mailboxConn) drop() {
	if c.cli != nil {
		_ = c.cli.Logout()
		c.cli = nil
	}
}

// folders returns INBOX plus the account's spam/junk folder, discovered once
// (verification mail for a new address often lands in spam).
func (c *mailboxConn) folders() []string {
	c.junkOnce.Do(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.cache = []string{"INBOX"}
		if err := c.ensure(); err != nil {
			return
		}
		if j := discoverJunk(c.cli); j != "" {
			c.cache = append(c.cache, j)
		}
	})
	return c.cache
}

var commonJunkNames = []string{"[Gmail]/Spam", "Spam", "Junk", "Junk E-mail", "Bulk Mail"}

// discoverJunk finds the junk folder via the SPECIAL-USE attribute, falling back
// to the names real providers use.
func discoverJunk(cli *client.Client) string {
	ch := make(chan *imap.MailboxInfo, 40)
	done := make(chan error, 1)
	go func() { done <- cli.List("", "*", ch) }()
	names := map[string]bool{}
	byAttr := ""
	for mb := range ch {
		names[mb.Name] = true
		for _, a := range mb.Attributes {
			if a == imap.JunkAttr {
				byAttr = mb.Name
			}
		}
	}
	<-done
	if byAttr != "" {
		return byAttr
	}
	for _, n := range commonJunkNames {
		if names[n] {
			return n
		}
	}
	return ""
}

// fetchRecent selects a folder and returns parsed messages since `since`,
// retrying up to three times with backoff and redialing on a failed attempt.
func (c *mailboxConn) fetchRecent(folder string, since time.Time) ([]parsedMsg, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			c.drop() // discard the known-bad connection before retrying
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		out, err := c.fetchOnce(folder, since)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *mailboxConn) fetchOnce(folder string, since time.Time) ([]parsedMsg, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	if _, err := c.cli.Select(folder, true); err != nil {
		return nil, err
	}
	criteria := imap.NewSearchCriteria()
	criteria.Since = since.Add(-24 * time.Hour) // SINCE is date-granular; be generous
	ids, err := c.cli.Search(criteria)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(ids...)
	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchInternalDate, imap.FetchEnvelope, section.FetchItem()}
	ch := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() { done <- c.cli.Fetch(seqset, items, ch) }()

	var out []parsedMsg
	for msg := range ch {
		pm := parseMessage(msg, section)
		if pm.id == "" {
			// No Message-ID header: fall back to a stable per-folder UID key so
			// claim-once still works.
			pm.id = fmt.Sprintf("%s#%d", folder, msg.Uid)
		}
		out = append(out, pm)
	}
	if err := <-done; err != nil {
		return out, err
	}
	return out, nil
}

// parsedMsg is a message reduced to what extraction needs.
type parsedMsg struct {
	id      string
	from    string
	subject string
	to      []string
	date    time.Time
	text    string
	html    string
}

func (m parsedMsg) toAlias(alias string) bool {
	want := strings.ToLower(strings.TrimSpace(alias))
	if want == "" {
		return false
	}
	// If the alias looks like a full address, parse addresses and compare the
	// mailbox exactly (so "me@x.com" does not match "notme@x.com" or an address
	// that merely contains it as a substring). A bare local-part alias still
	// matches by exact local-part.
	wantLocal, wantFull := splitAddr(want)
	for _, raw := range m.to {
		for _, field := range strings.Split(raw, ",") {
			addr := parseAddr(field)
			if addr == "" {
				continue
			}
			if addr == want {
				return true
			}
			if !wantFull {
				if local, _ := splitAddr(addr); local == wantLocal {
					return true
				}
			}
		}
	}
	return false
}

// parseAddr extracts the bare address from a header field via net/mail, falling
// back to a trimmed lowercase of the field.
func parseAddr(field string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		return ""
	}
	if a, err := netmail.ParseAddress(field); err == nil {
		return strings.ToLower(strings.TrimSpace(a.Address))
	}
	return strings.ToLower(field)
}

// splitAddr returns the local part and whether the value contains an "@".
func splitAddr(addr string) (local string, full bool) {
	if i := strings.IndexByte(addr, '@'); i >= 0 {
		return addr[:i], true
	}
	return addr, false
}

func parseMessage(msg *imap.Message, section *imap.BodySectionName) parsedMsg {
	pm := parsedMsg{date: msg.InternalDate}
	if msg.Envelope != nil {
		pm.subject = msg.Envelope.Subject
		pm.id = msg.Envelope.MessageId
		for _, a := range msg.Envelope.From {
			pm.from = a.Address()
			break
		}
		for _, a := range msg.Envelope.To {
			pm.to = append(pm.to, a.Address())
		}
	}
	r := msg.GetBody(section)
	if r == nil {
		return pm
	}
	mr, err := mail.CreateReader(r)
	if err != nil {
		raw, _ := io.ReadAll(r)
		pm.text = string(raw)
		return pm
	}
	// Delivered-To / X-Original-To tell the two aliases apart in one mailbox.
	for _, h := range []string{"Delivered-To", "X-Original-To"} {
		if v := mr.Header.Get(h); v != "" {
			pm.to = append(pm.to, v)
		}
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		if ih, ok := part.Header.(*mail.InlineHeader); ok {
			ct, _, _ := ih.ContentType()
			body, _ := io.ReadAll(part.Body)
			if strings.Contains(ct, "html") {
				pm.html += string(body)
			} else {
				pm.text += string(body)
			}
		}
	}
	if pm.text == "" && pm.html != "" {
		pm.text = stripTags(pm.html)
	}
	return pm
}

// stripTags is a crude HTML-to-text for extraction (links are also read from
// the raw HTML separately).
func stripTags(html string) string {
	var b strings.Builder
	depth := 0
	for _, r := range html {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
			b.WriteByte(' ')
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
