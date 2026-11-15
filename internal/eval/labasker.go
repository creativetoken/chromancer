package eval

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/lab"
)

// labAsker answers verification by polling the lab inbox and declines human
// handoff (eval scenarios do not require a human). It implements app.Asker.
type labAsker struct {
	base string
	app  string
}

// AskChallenge implements verify.Asker.
func (l *labAsker) AskChallenge(ctx context.Context, channel, account, hint string, deadline time.Time) (code, link string, approved bool, err error) {
	addr := lab.DefaultUser
	if l.app == "otp-sms" {
		addr = lab.PhoneFor
	}
	u := l.base + "/__lab/inbox?to=" + url.QueryEscape(addr)
	for {
		if c, lk, ok := l.poll(ctx, u); ok {
			return c, lk, false, nil
		}
		select {
		case <-time.After(150 * time.Millisecond):
		case <-ctx.Done():
			return "", "", false, ctx.Err()
		}
	}
}

// AskHuman implements captcha.Asker — eval declines (no human available).
func (l *labAsker) AskHuman(ctx context.Context, req captcha.HandoffRequest) error {
	return captcha.ErrHandoffTimeout
}

func (l *labAsker) poll(ctx context.Context, u string) (code, link string, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var msgs []lab.Message
	if json.Unmarshal(data, &msgs) != nil {
		return "", "", false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Link != "" {
			return "", msgs[i].Link, true
		}
		if msgs[i].Code != "" {
			return msgs[i].Code, "", true
		}
	}
	return "", "", false
}
