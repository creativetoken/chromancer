package captcha

import (
	"context"
	"strings"
	"testing"
	"time"
)

type capturingAsker struct {
	gotURL   string
	validMid bool
	h        *HumanHandoff
	token    string
}

func (c *capturingAsker) AskHuman(ctx context.Context, req HandoffRequest) error {
	c.gotURL = req.ViewURL
	// Extract the token the provider embedded and check it is valid *during* the
	// handoff (the view would use it now).
	if i := strings.Index(req.ViewURL, "token="); i >= 0 {
		rest := req.ViewURL[i+len("token="):]
		if j := strings.IndexAny(rest, "&"); j >= 0 {
			rest = rest[:j]
		}
		c.token = rest
		c.validMid = c.h.ValidToken(rest)
	}
	return nil
}

// TestHandoffViewProvider checks that Escalate uses ViewProvider to build the
// view URL, mints a token valid during the handoff, and revokes it afterwards —
// the integration point the camoufox VNC view plugs into.
func TestHandoffViewProvider(t *testing.T) {
	h := NewHumanHandoff(nil)
	a := &capturingAsker{h: h}
	h.Asker = a
	h.ViewProvider = func(token, challenge string) (string, error) {
		return "http://127.0.0.1:12345/vnc.html?token=" + token + "&challenge=" + challenge, nil
	}

	err := h.Escalate(context.Background(), ReasonStuck, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if !strings.Contains(a.gotURL, "/vnc.html?token=") {
		t.Fatalf("ViewProvider URL not used: %q", a.gotURL)
	}
	if !a.validMid {
		t.Error("token should be valid DURING the handoff (the view uses it then)")
	}
	if a.token == "" {
		t.Fatal("no token embedded")
	}
	if h.ValidToken(a.token) {
		t.Error("token should be revoked AFTER the handoff returns")
	}
}
