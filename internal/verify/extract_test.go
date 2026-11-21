package verify

import (
	"strings"
	"testing"
)

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

func TestExtractLinkHTMLEntities(t *testing.T) {
	// A query string split with &amp; must be decoded, not left as "&amp;".
	body := `<p>Confirm: <a href="https://app.example.com/verify?token=abc123&amp;next=%2Fhome">Verify your email</a></p>`
	got, ok := ExtractLink(body, "example.com")
	if !ok {
		t.Fatalf("no link found in %q", body)
	}
	if got != "https://app.example.com/verify?token=abc123&next=%2Fhome" {
		t.Fatalf("entity decode failed: got %q", got)
	}
}

func TestExtractLinkUTMStripped(t *testing.T) {
	// A verification link carrying utm_ params is kept (params stripped), not
	// rejected.
	body := "Verify: https://app.example.com/verify?token=abc123&utm_source=welcome&utm_medium=email"
	got, ok := ExtractLink(body, "example.com")
	if !ok {
		t.Fatal("a utm-tagged verification link must not be rejected")
	}
	if strings.Contains(got, "utm_") {
		t.Fatalf("utm params should be stripped, got %q", got)
	}
	if !strings.Contains(got, "token=abc123") {
		t.Fatalf("token must be preserved, got %q", got)
	}
}

func TestExtractLinkTrackerScoredByAnchor(t *testing.T) {
	// A click-tracking redirect whose URL is opaque is chosen on its anchor text.
	body := `<a href="https://click.sendgrid.net/ls/click?upn=OPAQUEHASH">Confirm your account</a>
<a href="https://click.sendgrid.net/ls/click?upn=OTHERHASH">Unsubscribe</a>`
	got, ok := ExtractLink(body, "")
	if !ok {
		t.Fatal("tracker link with a verification anchor should be selected")
	}
	if !strings.Contains(got, "OPAQUEHASH") {
		t.Fatalf("expected the 'Confirm your account' tracker link, got %q", got)
	}
}

func TestToAliasExactMatch(t *testing.T) {
	m := parsedMsg{to: []string{"Me <me@example.com>", "team@example.com"}}
	if !m.toAlias("me@example.com") {
		t.Error("exact address should match")
	}
	if m.toAlias("notme@example.com") {
		t.Error("a different full address must NOT match")
	}
	// A substring that is not a full address boundary must not match.
	if m.toAlias("e@example.com") {
		t.Error("substring of the local part must not match a full address")
	}
	// Bare local-part alias matches by exact local part.
	if !m.toAlias("team") {
		t.Error("bare local-part alias should match")
	}
	if m.toAlias("tea") {
		t.Error("partial local part must not match")
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
