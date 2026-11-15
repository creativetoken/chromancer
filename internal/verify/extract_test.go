package verify

import "testing"

func TestExtractCode(t *testing.T) {
	cases := []struct {
		text string
		want string
		ok   bool
	}{
		{"Your verification code is 482913", "482913", true},
		{"123456 is your Acme one-time passcode", "123456", true},
		{"Enter code G7HK2Q to continue", "G7HK2Q", true},
		{"Your OTP: 9021", "9021", true},
		// Phone number nearby must not be read as the code.
		{"Call us at +1 415 555 0199. Your code is 55123.", "55123", true},
		// Amounts and dates are not codes.
		{"You paid $45.00 on 12/08/2025", "", false},
		// No keyword and several numbers: ambiguous.
		{"Order 1001 shipped, tracking 889213 pieces 42", "", false},
	}
	for _, c := range cases {
		got, ok := ExtractCode(c.text)
		if ok != c.ok || (c.ok && got != c.want) {
			t.Errorf("ExtractCode(%q) = (%q,%v), want (%q,%v)", c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestExtractLink(t *testing.T) {
	body := `Welcome! Confirm your email:
https://app.example.com/verify?token=abc123xyz
Not you? https://email.example.com/unsubscribe?u=9
Follow us https://twitter.com/example`
	got, ok := ExtractLink(body, "example.com")
	if !ok || got != "https://app.example.com/verify?token=abc123xyz" {
		t.Fatalf("link = (%q,%v)", got, ok)
	}

	// Unsubscribe-only message yields nothing.
	if _, ok := ExtractLink("Manage prefs: https://x.com/unsubscribe?u=1", ""); ok {
		t.Fatal("unsubscribe link should not be selected")
	}

	// A bare magic link with a token wins even without the domain.
	got, ok = ExtractLink("Sign in: https://auth.other.net/magic?k=deadbeef", "")
	if !ok || got != "https://auth.other.net/magic?k=deadbeef" {
		t.Fatalf("magic link = (%q,%v)", got, ok)
	}
}

func TestClaimStore(t *testing.T) {
	s := NewClaimStore()
	if !s.Claim("sid-1") {
		t.Fatal("first claim should win")
	}
	if s.Claim("sid-1") {
		t.Fatal("second claim of same id should lose")
	}
	if !s.Claim("sid-2") {
		t.Fatal("different id should win")
	}
	if s.Claim("") != true {
		t.Fatal("empty id is always claimable")
	}
}
