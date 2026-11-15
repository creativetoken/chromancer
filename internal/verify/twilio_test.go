package verify

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockTwilio is an httptest Twilio REST API backed by an in-memory message list.
type mockTwilio struct {
	mu   sync.Mutex
	msgs []twilioMessage
}

func (m *mockTwilio) add(msg twilioMessage) {
	m.mu.Lock()
	m.msgs = append(m.msgs, msg)
	m.mu.Unlock()
}

func (m *mockTwilio) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/2010-04-01/Accounts/", func(w http.ResponseWriter, r *http.Request) {
		to := r.URL.Query().Get("To")
		m.mu.Lock()
		var out []twilioMessage
		for _, msg := range m.msgs {
			if sameNumber(msg.To, to) {
				out = append(out, msg)
			}
		}
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": out})
	})
	return httptest.NewServer(mux)
}

func newTwilio(base string, claims *ClaimStore) *TwilioProvider {
	return &TwilioProvider{
		AccountSID: "ACxxx", AuthToken: "tok", BaseURL: base,
		Poll: 10 * time.Millisecond, Claims: claims,
		NumberFor:  func(string) string { return "+441234567890" },
		TargetHint: func(string) string { return "acme" },
	}
}

func getWithDeadline(t *testing.T, p *TwilioProvider, req Request, d time.Duration) (Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req.Deadline = time.Now().Add(d)
	return p.GetCode(ctx, req)
}

func TestTwilioPicksMatchingMessage(t *testing.T) {
	mt := &mockTwilio{}
	// An unrelated message, then the real one from "Acme".
	mt.add(twilioMessage{SID: "SM1", From: "Bank", To: "+441234567890", Body: "Your bank balance is 4821", Direction: "inbound", DateSent: time.Now().Format(time.RFC1123Z)})
	mt.add(twilioMessage{SID: "SM2", From: "Acme", To: "+441234567890", Body: "Acme code: 902144", Direction: "inbound", DateSent: time.Now().Format(time.RFC1123Z)})
	srv := mt.server()
	defer srv.Close()
	p := newTwilio(srv.URL, NewClaimStore())
	res, err := getWithDeadline(t, p, Request{Channel: SMS, Account: "a"}, 2*time.Second)
	if err != nil || res.Code != "902144" {
		t.Fatalf("result = %+v err=%v", res, err)
	}
}

func TestTwilioRejectsStaleBySince(t *testing.T) {
	mt := &mockTwilio{}
	old := time.Now().Add(-10 * time.Minute)
	mt.add(twilioMessage{SID: "SM1", From: "Acme", To: "+441234567890", Body: "Acme code 111111", Direction: "inbound", DateSent: old.Format(time.RFC1123Z)})
	srv := mt.server()
	defer srv.Close()
	p := newTwilio(srv.URL, NewClaimStore())
	// Since is now; the only message is 10 minutes old, so it must time out.
	if _, err := getWithDeadline(t, p, Request{Channel: SMS, Account: "a", Since: time.Now()}, 300*time.Millisecond); err != ErrTimeout {
		t.Fatalf("expected timeout for stale message, got %v", err)
	}
}

func TestTwilioSkipsClaimed(t *testing.T) {
	mt := &mockTwilio{}
	mt.add(twilioMessage{SID: "SM9", From: "Acme", To: "+441234567890", Body: "Acme code 424242", Direction: "inbound", DateSent: time.Now().Format(time.RFC1123Z)})
	srv := mt.server()
	defer srv.Close()
	claims := NewClaimStore()
	claims.Claim("SM9") // already used by a prior verification
	p := newTwilio(srv.URL, claims)
	if _, err := getWithDeadline(t, p, Request{Channel: SMS, Account: "a"}, 300*time.Millisecond); err != ErrTimeout {
		t.Fatalf("expected timeout when the only message is claimed, got %v", err)
	}
}

func TestTwilioResendAtMidpoint(t *testing.T) {
	mt := &mockTwilio{}
	srv := mt.server()
	defer srv.Close()
	var resent int32
	p := newTwilio(srv.URL, NewClaimStore())
	p.OnResend = func(context.Context) error {
		// Deliver the code only after resend is requested.
		mt.add(twilioMessage{SID: "SMr", From: "Acme", To: "+441234567890", Body: "Acme code 777000", Direction: "inbound", DateSent: time.Now().Format(time.RFC1123Z)})
		resent++
		return nil
	}
	res, err := getWithDeadline(t, p, Request{Channel: SMS, Account: "a"}, 2*time.Second)
	if err != nil || res.Code != "777000" {
		t.Fatalf("result = %+v err=%v", res, err)
	}
	if resent != 1 {
		t.Fatalf("resend called %d times, want 1", resent)
	}
}

func TestTwilioAlphanumericCode(t *testing.T) {
	mt := &mockTwilio{}
	mt.add(twilioMessage{SID: "SMa", From: "Acme", To: "+441234567890", Body: "Your verification code is G7HK2Q", Direction: "inbound", DateSent: time.Now().Format(time.RFC1123Z)})
	srv := mt.server()
	defer srv.Close()
	p := newTwilio(srv.URL, NewClaimStore())
	res, err := getWithDeadline(t, p, Request{Channel: SMS, Account: "a"}, time.Second)
	if err != nil || res.Code != "G7HK2Q" {
		t.Fatalf("result = %+v err=%v", res, err)
	}
}

func TestTwilioSignatureValidation(t *testing.T) {
	token := "secret-token"
	u := "https://hook.example.com/sms"
	form := url.Values{"MessageSid": {"SM1"}, "From": {"+100"}, "To": {"+441234567890"}, "Body": {"code 123456"}}

	// Build the valid signature the way Twilio does.
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(u)
	for _, k := range keys {
		for _, v := range form[k] {
			b.WriteString(k + v)
		}
	}
	mac := hmac.New(sha1.New, []byte(token))
	mac.Write([]byte(b.String()))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !ValidateTwilioSignature(token, u, form, sig) {
		t.Fatal("valid signature rejected")
	}
	if ValidateTwilioSignature(token, u, form, "tampered") {
		t.Fatal("bad signature accepted")
	}
	if ValidateTwilioSignature("wrong-token", u, form, sig) {
		t.Fatal("signature accepted under the wrong token")
	}
}

func TestIsPhoneRejected(t *testing.T) {
	yes := []string{"VoIP numbers are not supported", "Please enter a valid phone number", "That number is invalid"}
	for _, s := range yes {
		if !IsPhoneRejected(s) {
			t.Errorf("should be rejected: %q", s)
		}
	}
	if IsPhoneRejected("We sent a code to your phone") {
		t.Error("normal message flagged as rejection")
	}
}
