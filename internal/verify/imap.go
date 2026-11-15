package verify

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
)

// MailboxDef is the connection detail for one IMAP mailbox.
type MailboxDef struct {
	ID   string
	Host string
	Port int
	User string
	Pass string
}

func (m MailboxDef) addr() string { return fmt.Sprintf("%s:%d", m.Host, m.Port) }

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

func (p *IMAPProvider) folders() []string {
	if len(p.Folders) > 0 {
		return p.Folders
	}
	return []string{"INBOX", "[Gmail]/Spam"}
}

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
	for {
		for _, folder := range p.folders() {
			msgs, err := conn.fetchRecent(folder, since)
			if err != nil {
				continue
			}
			for _, m := range msgs {
				if !m.toAlias(alias) || !m.date.After(since) {
					continue
				}
				if p.Claims != nil && p.Claims.Claimed(m.id) {
					continue
				}
				if res, ok := p.extract(ctx, req, m, domain); ok {
					if p.Claims != nil {
						p.Claims.Claim(m.id)
					}
					return res, nil
				}
			}
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
	linkText := m.text + "\n" + m.html
	if req.WantLink {
		if link, ok := ExtractLink(linkText, domain); ok {
			return Result{Kind: ResLink, Link: link}, true
		}
	}
	if code, ok := ExtractCode(m.text); ok {
		return Result{Kind: ResCode, Code: code}, true
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
	if err := c.Login(mb.User, mb.Pass); err != nil {
		_ = c.Logout()
		return nil, err
	}
	return c, nil
}

// mailboxConn is one shared, serialized IMAP connection with reconnect.
type mailboxConn struct {
	mu   sync.Mutex
	mb   MailboxDef
	cli  *client.Client
	dial func(mb MailboxDef) (*client.Client, error)
}

func (c *mailboxConn) ensure() error {
	if c.cli != nil {
		return nil
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

// fetchRecent selects a folder and returns parsed messages since `since`,
// reconnecting once if the connection has dropped.
func (c *mailboxConn) fetchRecent(folder string, since time.Time) ([]parsedMsg, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out, err := c.fetchOnce(folder, since)
	if err != nil {
		// Reconnect and retry once.
		c.drop()
		if err2 := c.ensure(); err2 != nil {
			return nil, err2
		}
		return c.fetchOnce(folder, since)
	}
	return out, nil
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
	alias = strings.ToLower(strings.TrimSpace(alias))
	for _, a := range m.to {
		if strings.Contains(strings.ToLower(a), alias) {
			return true
		}
	}
	return false
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
