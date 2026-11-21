package verify

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// TwilioProvider reads inbound SMS for an account's number through the Twilio
// REST API (no SDK, to keep dependencies small). It polls for a message newer
// than Since, picks the one that matches the target, extracts the code, and
// claims it so it is used once.
type TwilioProvider struct {
	AccountSID string
	AuthToken  string
	BaseURL    string // default https://api.twilio.com
	HTTP       *http.Client
	Poll       time.Duration
	Skew       time.Duration // clock-skew allowance subtracted from Since
	Claims     *ClaimStore

	// NumberFor maps an account to its E.164 number ("" if it has none).
	NumberFor func(account string) string
	// TargetHint returns a brand/domain string to prefer in matching ("" = none).
	TargetHint func(account string) string
	// OnResend is called once near the midpoint if nothing has arrived (the flow
	// handler clicks "resend"); optional.
	OnResend func(ctx context.Context) error
	// Ambiguous is an optional LLM fallback given a single message body.
	Ambiguous func(ctx context.Context, body string) (string, bool)
}

// twilioMessage is one message from the REST API.
type twilioMessage struct {
	SID       string `json:"sid"`
	From      string `json:"from"`
	To        string `json:"to"`
	Body      string `json:"body"`
	Direction string `json:"direction"`
	DateSent  string `json:"date_sent"`
}

func (m twilioMessage) sent() time.Time {
	for _, f := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339} {
		if t, err := time.Parse(f, m.DateSent); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Supports implements Provider: SMS, when the account has a number.
func (p *TwilioProvider) Supports(ch Channel, account string) bool {
	return ch == SMS && p.NumberFor != nil && p.NumberFor(account) != ""
}

// Resend implements Provider (a no-op; resend during a wait uses OnResend).
func (p *TwilioProvider) Resend(context.Context, Request) error { return nil }

// GetCode polls for a matching inbound SMS and returns its code.
func (p *TwilioProvider) GetCode(ctx context.Context, req Request) (Result, error) {
	number := p.NumberFor(req.Account)
	if number == "" {
		return Result{}, ErrUnsupported
	}
	poll := p.Poll
	if poll == 0 {
		poll = 3 * time.Second
	}
	since := req.Since.Add(-p.skew())
	start := time.Now()
	var resendAt time.Time
	if !req.Deadline.IsZero() {
		resendAt = start.Add(req.Deadline.Sub(start) / 2)
	}
	resent := false

	for {
		msgs, err := p.list(ctx, number)
		if err == nil {
			if m, code, ok := p.pick(ctx, req, msgs, since); ok {
				p.claim(m.SID)
				return Result{Kind: ResCode, Code: code, MsgID: m.SID}, nil
			}
		}
		if !resent && p.OnResend != nil && !resendAt.IsZero() && time.Now().After(resendAt) {
			_ = p.OnResend(ctx)
			resent = true
		}
		select {
		case <-time.After(poll):
		case <-ctx.Done():
			return Result{}, ErrTimeout
		}
	}
}

func (p *TwilioProvider) skew() time.Duration {
	if p.Skew == 0 {
		return 5 * time.Second
	}
	return p.Skew
}

func (p *TwilioProvider) claim(sid string) {
	if p.Claims != nil {
		p.Claims.Claim(sid)
	}
}

// Unclaim releases a claimed SID so a retry can read the same code again (used
// when the code was fetched but could not be entered). Implements
// verify.Unclaimer.
func (p *TwilioProvider) Unclaim(sid string) {
	if p.Claims != nil {
		p.Claims.Unclaim(sid)
	}
}

// pick chooses the best unclaimed inbound message to our number after since and
// extracts a code. It prefers messages whose sender or body matches the target.
func (p *TwilioProvider) pick(ctx context.Context, req Request, msgs []twilioMessage, since time.Time) (twilioMessage, string, bool) {
	var cands []twilioMessage
	for _, m := range msgs {
		if !strings.EqualFold(strings.ToLower(m.Direction), "inbound") && m.Direction != "" {
			continue
		}
		if !sameNumber(m.To, req.hintNumber(p)) {
			continue
		}
		if t := m.sent(); !t.IsZero() && !t.After(since) {
			continue
		}
		if p.Claims != nil && p.Claims.Claimed(m.SID) {
			continue
		}
		cands = append(cands, m)
	}
	// Newest first.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].sent().After(cands[j].sent()) })
	hint := ""
	if p.TargetHint != nil {
		hint = strings.ToLower(p.TargetHint(req.Account))
	}
	if hint == "" {
		hint = strings.ToLower(req.Hint)
	}
	// Target-matching candidates first, keeping newest-first order within each.
	if hint != "" {
		sort.SliceStable(cands, func(i, j int) bool {
			return matchesHint(cands[i], hint) && !matchesHint(cands[j], hint)
		})
	}
	for _, m := range cands {
		if code, ok := ExtractCode(m.Body); ok {
			return m, code, true
		}
		if p.Ambiguous != nil {
			if code, ok := p.Ambiguous(ctx, m.Body); ok {
				return m, code, true
			}
		}
	}
	return twilioMessage{}, "", false
}

func (r Request) hintNumber(p *TwilioProvider) string {
	if p.NumberFor != nil {
		return p.NumberFor(r.Account)
	}
	return ""
}

func matchesHint(m twilioMessage, hint string) bool {
	return strings.Contains(strings.ToLower(m.From), hint) || strings.Contains(strings.ToLower(m.Body), hint)
}

// sameNumber compares E.164 numbers ignoring spaces and punctuation.
func sameNumber(a, b string) bool {
	return normNumber(a) == normNumber(b) && normNumber(a) != ""
}

var nonDigitPlus = regexp.MustCompile(`[^\d+]`)

func normNumber(s string) string { return nonDigitPlus.ReplaceAllString(s, "") }

// list fetches recent messages to our number via the REST API.
func (p *TwilioProvider) list(ctx context.Context, to string) ([]twilioMessage, error) {
	base := p.BaseURL
	if base == "" {
		base = "https://api.twilio.com"
	}
	u := fmt.Sprintf("%s/2010-04-01/Accounts/%s/Messages.json?To=%s&PageSize=20",
		strings.TrimRight(base, "/"), url.PathEscape(p.AccountSID), url.QueryEscape(to))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	httpReq.SetBasicAuth(p.AccountSID, p.AuthToken)
	cl := p.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := cl.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("twilio: http %d", resp.StatusCode)
	}
	var out struct {
		Messages []twilioMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// phoneRejectedRe matches pages that reject the number.
var phoneRejectedRe = regexp.MustCompile(`(?i)(voip numbers? (are )?not supported|number (is )?(invalid|not valid|not supported)|enter a valid (phone|mobile) number|unable to send.*code)`)

// IsPhoneRejected reports whether page text says the phone number was refused.
func IsPhoneRejected(pageText string) bool { return phoneRejectedRe.MatchString(pageText) }

// ---- Inbound webhook (optional; off by default) ----

// TwilioWebhook queues inbound SMS pushed by Twilio's webhook, validating the
// request signature. It is a Provider for channel=sms when enabled.
type TwilioWebhook struct {
	AuthToken string
	PublicURL string // the exact URL Twilio is configured to POST to (for signing)
	Claims    *ClaimStore
	NumberFor func(account string) string

	queue chan twilioMessage
}

// NewTwilioWebhook builds a webhook queue.
func NewTwilioWebhook(authToken, publicURL string) *TwilioWebhook {
	return &TwilioWebhook{AuthToken: authToken, PublicURL: publicURL, queue: make(chan twilioMessage, 64)}
}

// Handler validates the signature and enqueues the message.
func (h *TwilioWebhook) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if !ValidateTwilioSignature(h.AuthToken, h.PublicURL, r.PostForm, r.Header.Get("X-Twilio-Signature")) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		m := twilioMessage{SID: r.PostForm.Get("MessageSid"), From: r.PostForm.Get("From"), To: r.PostForm.Get("To"),
			Body: r.PostForm.Get("Body"), Direction: "inbound", DateSent: time.Now().Format(time.RFC1123Z)}
		select {
		case h.queue <- m:
		default:
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, "<Response></Response>")
	}
}

// ValidateTwilioSignature checks Twilio's X-Twilio-Signature: base64 of
// HMAC-SHA1(authToken, url + sorted(key+value) of POST params).
func ValidateTwilioSignature(authToken, fullURL string, form url.Values, sig string) bool {
	if authToken == "" || sig == "" {
		return false
	}
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(fullURL)
	for _, k := range keys {
		for _, v := range form[k] {
			b.WriteString(k)
			b.WriteString(v)
		}
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}
