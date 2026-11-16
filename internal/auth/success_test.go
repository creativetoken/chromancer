package auth

import (
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
)

// Freshservice served only infrastructure/session cookies on a logged-out
// bot-challenge page: a bare session cookie (_itildesk_session) plus HTTPOnly
// Cloudflare (__cf_bm) and reCAPTCHA (_GRECAPTCHA) cookies. When validating a
// RESTORED session these were injected by us, so against the empty baseline they
// all look "new" — but none proves login and they must not count as strong
// evidence. Only a live login (trustStorage=true) may trust freshly-minted
// cookies.
func TestStorageEvidenceRestoredSessionNotTrusted(t *testing.T) {
	cur := browser.SessionState{Cookies: []browser.Cookie{
		{Name: "_itildesk_session", Value: "abc", HTTPOnly: true},
		{Name: "__cf_bm", Value: "def", HTTPOnly: true},
		{Name: "_GRECAPTCHA", Value: "ghi", HTTPOnly: true},
		{Name: "_x_m", Value: "1"},
		{Name: "_x_d", Value: "2"},
	}}
	base := Baseline{} // restore path uses an empty baseline

	if _, strong := storageEvidence(cur, base, false); strong {
		t.Error("restored infrastructure cookies must not be strong evidence of login")
	}
	if _, strong := storageEvidence(cur, base, true); !strong {
		t.Error("a live login should still trust a freshly-minted session cookie")
	}
}

func TestJWTExpiry(t *testing.T) {
	// {"alg":"HS256"}.{"exp":1700000000}.sig
	tok := "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE3MDAwMDAwMDAsInN1YiI6IjEifQ.abc"
	exp := JWTExpiry("Bearer " + tok)
	if exp == nil || exp.Unix() != 1700000000 {
		t.Fatalf("exp: %v", exp)
	}
	if JWTExpiry("not a jwt", "also-not") != nil {
		t.Fatal("non-jwt should give nil")
	}
	if JWTExpiry() != nil {
		t.Fatal("empty")
	}
	_ = time.Now
}
