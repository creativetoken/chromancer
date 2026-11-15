package verify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTOTPMatchesKnownVector(t *testing.T) {
	// RFC 6238 test seed "12345678901234567890" in base32.
	seed := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	// At T=59s, the RFC SHA1 8-digit value is 94287082; 6 digits -> 287082.
	code, err := Code(seed, time.Unix(59, 0), 6, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("totp got %s want 287082", code)
	}
}

func TestTOTPProvider(t *testing.T) {
	p := NewTOTP(func(a string) string {
		if a == "alice" {
			return "JBSWY3DPEHPK3PXP"
		}
		return ""
	})
	if !p.Supports(TOTP, "alice") || p.Supports(Email, "alice") || p.Supports(TOTP, "bob") {
		t.Fatal("supports wrong")
	}
	r, err := p.GetCode(context.Background(), Request{Channel: TOTP, Account: "alice"})
	if err != nil || r.Kind != ResCode || len(r.Code) != 6 {
		t.Fatalf("totp code: %+v %v", r, err)
	}
}

func TestChainRouting(t *testing.T) {
	totp := NewTOTP(func(string) string { return "JBSWY3DPEHPK3PXP" })
	wh := NewWebhook()
	c := Chain{totp, wh}
	if !c.Supports(TOTP, "a") || !c.Supports(Email, "a") {
		t.Fatal("chain supports")
	}
	// email routed to webhook
	go func() {
		time.Sleep(20 * time.Millisecond)
		wh.Deliver("a", Result{Kind: ResCode, Code: "111222"})
	}()
	r, err := c.GetCode(context.Background(), Request{Channel: Email, Account: "a"})
	if err != nil || r.Code != "111222" {
		t.Fatalf("webhook via chain: %+v %v", r, err)
	}
}

func TestWebhookHandlerAndTimeout(t *testing.T) {
	wh := NewWebhook()
	srv := httptest.NewServer(wh.Handler())
	defer srv.Close()
	go func() {
		time.Sleep(20 * time.Millisecond)
		http.Get(srv.URL + "?account=bob&link=http://x/callback")
	}()
	r, err := wh.GetCode(context.Background(), Request{Channel: Email, Account: "bob"})
	if err != nil || r.Kind != ResLink || r.Link != "http://x/callback" {
		t.Fatalf("webhook link: %+v %v", r, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := wh.GetCode(ctx, Request{Channel: Email, Account: "nobody"}); err != ErrTimeout {
		t.Fatalf("want timeout, got %v", err)
	}
}

func TestCLIProvider(t *testing.T) {
	var out strings.Builder
	p := NewCLI(strings.NewReader("123456\n"), &out)
	r, err := p.GetCode(context.Background(), Request{Channel: Email, Account: "a", Hint: "sent to ***1234"})
	if err != nil || r.Code != "123456" {
		t.Fatalf("cli code: %+v %v", r, err)
	}
	if !strings.Contains(out.String(), "***1234") {
		t.Fatalf("hint not shown: %s", out.String())
	}
	p2 := NewCLI(strings.NewReader("http://x/magic\n"), &out)
	r2, _ := p2.GetCode(context.Background(), Request{Channel: Email})
	if r2.Kind != ResLink || r2.Link != "http://x/magic" {
		t.Fatalf("cli link: %+v", r2)
	}
}

type fakeAsk struct {
	code string
}

func (f fakeAsk) AskChallenge(ctx context.Context, channel, account, hint string, dl time.Time) (string, string, bool, error) {
	return f.code, "", false, nil
}

func TestAskProvider(t *testing.T) {
	p := NewAsk(fakeAsk{code: "909090"}, Email, SMS)
	if !p.Supports(Email, "a") || p.Supports(TOTP, "a") {
		t.Fatal("ask supports")
	}
	r, err := p.GetCode(context.Background(), Request{Channel: Email, Account: "a"})
	if err != nil || r.Code != "909090" {
		t.Fatalf("ask: %+v %v", r, err)
	}
}

func TestLabInbox(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"to":"alice@example.com","channel":"email","code":"445566","at":"2999-01-01T00:00:00Z"}]`))
	}))
	defer srv.Close()
	li := NewLabInbox(srv.URL, nil)
	li.Poll = 5 * time.Millisecond
	r, err := li.GetCode(context.Background(), Request{Channel: Email, Account: "alice@example.com"})
	if err != nil || r.Code != "445566" {
		t.Fatalf("labinbox: %+v %v", r, err)
	}
}
