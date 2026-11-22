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

// Bambu Lab served only consent-manager (TAconsentID, cmapi_*, notice_*),
// CDN/bot (cf_clearance, __cf_bm), CSRF (bbl_csrf_token) and browser-id
// (__bbl_bid) cookies to a logged-out visitor who never signed in. Before the
// fix, authCookieRe's bare "id$" matched TAconsentID and the HTTPOnly branch
// matched cf_clearance, so a live login (trustStorage=true) was wrongly judged
// strong — overriding the agent's request_human into a bogus "authenticated".
// None of these is proof of login; only a real session/auth cookie is.
func TestStorageEvidenceConsentAndCDNCookiesNotAuth(t *testing.T) {
	loggedOut := browser.SessionState{
		Cookies: []browser.Cookie{
			{Name: "__bbl_bid", Value: "b1"},
			{Name: "TAsessionID", Value: "s1", Secure: true},
			{Name: "bbl_csrf_token", Value: "c1", Secure: true},
			{Name: "cf_clearance", Value: "cf", Secure: true, HTTPOnly: true},
			{Name: "__cf_bm", Value: "bm", Secure: true, HTTPOnly: true},
			{Name: "TAconsentID", Value: "ta", Secure: true},
			{Name: "notice_gdpr_prefs", Value: "n1", Secure: true},
			{Name: "cmapi_gtm_bl", Value: "g1", Secure: true},
		},
		LocalStorage: map[string]string{"truste.cookie.TAconsentID": "x"},
	}
	ev, strong := storageEvidence(loggedOut, Baseline{}, true /* live login */)
	if strong {
		t.Errorf("consent/CDN/CSRF cookies must not be strong login evidence; got %v", ev)
	}

	// A genuine login still reads as strong.
	authed := browser.SessionState{Cookies: []browser.Cookie{
		{Name: "token", Value: "t", HTTPOnly: true},
		{Name: "refreshToken", Value: "r", HTTPOnly: true},
		{Name: "cf_clearance", Value: "cf"}, // noise alongside the real tokens
	}}
	if _, strong := storageEvidence(authed, Baseline{}, true); !strong {
		t.Error("real token/refreshToken cookies must count as strong login evidence")
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
