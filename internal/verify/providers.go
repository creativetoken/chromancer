package verify

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- CLI prompt

// CLIProvider prompts a human operator for the code/link on a terminal. It
// supports every channel (the operator reads it from wherever it arrived).
type CLIProvider struct {
	In  io.Reader
	Out io.Writer
	mu  sync.Mutex
}

// NewCLI builds a CLIProvider.
func NewCLI(in io.Reader, out io.Writer) *CLIProvider { return &CLIProvider{In: in, Out: out} }

// Supports implements Provider.
func (p *CLIProvider) Supports(Channel, string) bool { return true }

// GetCode implements Provider by prompting and reading one line.
func (p *CLIProvider) GetCode(ctx context.Context, req Request) (Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	hint := req.Hint
	if hint != "" {
		hint = " (" + hint + ")"
	}
	noun := "code"
	kind := ResCode
	if req.Channel == Email {
		noun = "code or magic link"
	}
	fmt.Fprintf(p.Out, "[verification] enter the %s for %s via %s%s: ", noun, req.Account, req.Channel, hint)

	type line struct {
		s   string
		err error
	}
	ch := make(chan line, 1)
	go func() {
		sc := bufio.NewScanner(p.In)
		if sc.Scan() {
			ch <- line{s: strings.TrimSpace(sc.Text())}
		} else {
			ch <- line{err: io.EOF}
		}
	}()
	select {
	case l := <-ch:
		if l.err != nil {
			return Result{}, ErrDeclined
		}
		if l.s == "" {
			return Result{}, ErrDeclined
		}
		if strings.HasPrefix(l.s, "http://") || strings.HasPrefix(l.s, "https://") {
			return Result{Kind: ResLink, Link: l.s}, nil
		}
		return Result{Kind: kind, Code: l.s}, nil
	case <-ctx.Done():
		return Result{}, ErrTimeout
	}
}

// Resend prints a note (the operator triggers resend out of band).
func (p *CLIProvider) Resend(_ context.Context, req Request) error {
	fmt.Fprintf(p.Out, "[verification] (resend requested for %s)\n", req.Account)
	return nil
}

// ---------------------------------------------------------------- Ask bridge

// Asker surfaces a pending challenge and blocks for an answer. The server's
// prompter satisfies it; this bridges verification to the MCP pending/answer.
type Asker interface {
	AskChallenge(ctx context.Context, channel, account, hint string, deadline time.Time) (code, link string, approved bool, err error)
}

// AskProvider obtains codes via an Asker (MCP pending/answer).
type AskProvider struct {
	Ask      Asker
	Channels map[Channel]bool // which channels to claim; nil = all
}

// NewAsk builds an AskProvider for the given channels (nil = all).
func NewAsk(ask Asker, channels ...Channel) *AskProvider {
	var m map[Channel]bool
	if len(channels) > 0 {
		m = map[Channel]bool{}
		for _, c := range channels {
			m[c] = true
		}
	}
	return &AskProvider{Ask: ask, Channels: m}
}

// Supports implements Provider.
func (p *AskProvider) Supports(ch Channel, _ string) bool {
	return p.Ask != nil && (p.Channels == nil || p.Channels[ch])
}

// GetCode implements Provider by surfacing the pending challenge.
func (p *AskProvider) GetCode(ctx context.Context, req Request) (Result, error) {
	code, link, approved, err := p.Ask.AskChallenge(ctx, string(req.Channel), req.Account, req.Hint, req.Deadline)
	if err != nil {
		return Result{}, err
	}
	switch {
	case approved:
		return Result{Kind: ResApproved}, nil
	case link != "":
		return Result{Kind: ResLink, Link: link}, nil
	case code != "":
		return Result{Kind: ResCode, Code: code}, nil
	default:
		return Result{}, ErrDeclined
	}
}

// Resend is a no-op (the operator triggers delivery).
func (p *AskProvider) Resend(context.Context, Request) error { return nil }

// ---------------------------------------------------------------- Webhook

// WebhookProvider waits for the operator's system to POST the code/link to a
// local endpoint keyed by account. Delivery is in-process via Deliver (the
// operator's HTTP handler calls it) so no network is required for tests.
type WebhookProvider struct {
	mu      sync.Mutex
	waiters map[string]chan Result
}

// NewWebhook builds a WebhookProvider.
func NewWebhook() *WebhookProvider { return &WebhookProvider{waiters: map[string]chan Result{}} }

// Supports implements Provider (email/sms/push via webhook).
func (p *WebhookProvider) Supports(ch Channel, _ string) bool {
	return ch == Email || ch == SMS || ch == Push
}

// GetCode blocks until Deliver is called for the account or ctx/Deadline ends.
func (p *WebhookProvider) GetCode(ctx context.Context, req Request) (Result, error) {
	ch := make(chan Result, 1)
	p.mu.Lock()
	p.waiters[req.Account] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiters, req.Account)
		p.mu.Unlock()
	}()
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return Result{}, ErrTimeout
	}
}

// Deliver pushes a result to a waiting GetCode for account.
func (p *WebhookProvider) Deliver(account string, r Result) bool {
	p.mu.Lock()
	ch, ok := p.waiters[account]
	p.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- r:
		return true
	default:
		return false
	}
}

// Handler is an http.Handler the operator can mount: POST ?account=..&code=..|link=..
func (p *WebhookProvider) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account := r.URL.Query().Get("account")
		code := r.URL.Query().Get("code")
		link := r.URL.Query().Get("link")
		res := Result{Kind: ResCode, Code: code}
		if link != "" {
			res = Result{Kind: ResLink, Link: link}
		}
		if p.Deliver(account, res) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// Resend is a no-op.
func (p *WebhookProvider) Resend(context.Context, Request) error { return nil }

// ---------------------------------------------------------------- lab inbox

// LabInbox polls the lab's inbox endpoint for messages and extracts a code or
// link. Used by tests; it reaches only the local lab server.
type LabInbox struct {
	BaseURL string // e.g. http://127.0.0.1:8089
	Addr    func(account string) string
	HTTP    *http.Client
	Poll    time.Duration
}

// NewLabInbox builds a LabInbox. addr maps account -> the address the code was
// sent to (email or phone).
func NewLabInbox(base string, addr func(account string) string) *LabInbox {
	return &LabInbox{BaseURL: strings.TrimRight(base, "/"), Addr: addr, HTTP: &http.Client{Timeout: 5 * time.Second}, Poll: 300 * time.Millisecond}
}

// Supports implements Provider (email + sms via the inbox).
func (l *LabInbox) Supports(ch Channel, _ string) bool { return ch == Email || ch == SMS }

type labMsg struct {
	To      string    `json:"to"`
	Channel string    `json:"channel"`
	Code    string    `json:"code"`
	Link    string    `json:"link"`
	At      time.Time `json:"at"`
}

// GetCode polls the inbox for a message newer than Since.
func (l *LabInbox) GetCode(ctx context.Context, req Request) (Result, error) {
	addr := req.Account
	if l.Addr != nil {
		addr = l.Addr(req.Account)
	}
	u := l.BaseURL + "/__lab/inbox?to=" + url.QueryEscape(addr)
	poll := l.Poll
	if poll == 0 {
		poll = 300 * time.Millisecond
	}
	for {
		msgs, err := l.fetch(ctx, u)
		if err == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				m := msgs[i]
				if !req.Since.IsZero() && !m.At.After(req.Since) {
					continue
				}
				if m.Link != "" {
					return Result{Kind: ResLink, Link: m.Link}, nil
				}
				if m.Code != "" {
					return Result{Kind: ResCode, Code: m.Code}, nil
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

func (l *LabInbox) fetch(ctx context.Context, u string) ([]labMsg, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var msgs []labMsg
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

// Resend is a no-op (the lab re-sends on form action).
func (l *LabInbox) Resend(context.Context, Request) error { return nil }
