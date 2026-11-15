package handback

import "testing"

func TestTokenSubmitted(t *testing.T) {
	r := NewRecorder()
	tok := "lab-captcha-ok-0123456789"
	// Craft a captured login POST carrying the token in its body.
	var e HAREntry
	e.Request.Method = "POST"
	e.Request.URL = "https://app.test/login"
	e.Request.Type = "Document"
	e.Request.PostData = "email=alice%40example.com&password=x&captcha-token=" + tok
	r.rec.Entries = append(r.rec.Entries, e)

	if !r.TokenSubmitted(tok) {
		t.Fatal("token in post body should be reported as submitted")
	}
	if r.TokenSubmitted("never-sent-token-xyz") {
		t.Fatal("absent token must not be reported as submitted")
	}
	if r.TokenSubmitted("short") {
		t.Fatal("too-short values must be ignored to avoid coincidental matches")
	}

	// Also detect a token carried in a request header.
	var h HAREntry
	h.Request.Method = "GET"
	h.Request.Type = "XHR"
	h.Request.Headers = []HARHeader{{Name: "X-Captcha-Token", Value: tok}}
	r.rec.Entries = append(r.rec.Entries, h)
	if !r.TokenSubmitted(tok) {
		t.Fatal("token in a request header should be reported as submitted")
	}
}
