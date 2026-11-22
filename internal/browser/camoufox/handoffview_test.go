package camoufox

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestViewProxyRefusesWithoutToken(t *testing.T) {
	// Upstream that would answer if ever reached.
	var reached bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("vnc"))
	}))
	defer upstream.Close()
	uURL, _ := url.Parse(upstream.URL)

	good := "secret-token-123"
	vp := NewViewProxy(uURL, func(tok string) bool { return tok == good })
	front := httptest.NewServer(vp)
	defer front.Close()

	cases := []struct {
		name       string
		url        string
		headers    map[string]string
		wantStatus int
		wantReach  bool
	}{
		{"no token", front.URL + "/", nil, http.StatusUnauthorized, false},
		{"empty token", front.URL + "/?token=", nil, http.StatusUnauthorized, false},
		{"wrong token", front.URL + "/?token=nope", nil, http.StatusUnauthorized, false},
		{"good token query", front.URL + "/?token=" + good, nil, http.StatusOK, true},
		{"good token header", front.URL + "/", map[string]string{"X-Handoff-Token": good}, http.StatusOK, true},
		// A WebSocket upgrade WITHOUT a token must be refused before proxying.
		{"ws upgrade no token", front.URL + "/websockify", map[string]string{
			"Connection": "Upgrade", "Upgrade": "websocket",
			"Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
		}, http.StatusUnauthorized, false},
		// A WebSocket upgrade carrying the token in the subprotocol passes the gate.
		{"ws upgrade subprotocol token", front.URL + "/websockify", map[string]string{
			"Connection": "Upgrade", "Upgrade": "websocket",
			"Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
			"Sec-WebSocket-Protocol": "handoff." + good,
		}, http.StatusOK, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reached = false
			req, _ := http.NewRequest(http.MethodGet, c.url, nil)
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.wantStatus)
			}
			if reached != c.wantReach {
				t.Errorf("upstream reached = %v, want %v (token must gate BEFORE proxying)", reached, c.wantReach)
			}
		})
	}
}

// TestViewProxySetsCookieForSubResources verifies that an authenticated request
// sets the handoff cookie, and that a follow-up request carrying ONLY that cookie
// (as noVNC's relative-URL CSS/JS/image fetches do — no ?token=) is let through.
// Without this, sub-resources 401 and the page renders broken.
func TestViewProxySetsCookieForSubResources(t *testing.T) {
	var reached bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	uURL, _ := url.Parse(upstream.URL)
	good := "secret-token-123"
	front := httptest.NewServer(NewViewProxy(uURL, func(tok string) bool { return tok == good }))
	defer front.Close()

	// First request with the query token: must succeed AND set the cookie.
	resp, err := http.Get(front.URL + "/vnc.html?token=" + good)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("vnc.html status = %d, want 200", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == handoffCookie {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != good {
		t.Fatalf("expected %s cookie carrying the token, got %+v", handoffCookie, resp.Cookies())
	}

	// A sub-resource request with NO query token but WITH the cookie must pass.
	reached = false
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/app/styles/base.css", nil)
	req.AddCookie(cookie)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sub-resource request: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || !reached {
		t.Errorf("sub-resource via cookie: status = %d reached = %v, want 200/true", resp2.StatusCode, reached)
	}

	// The same sub-resource with neither token nor cookie is still refused.
	reached = false
	resp3, err := http.Get(front.URL + "/app/styles/base.css")
	if err != nil {
		t.Fatalf("no-auth request: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized || reached {
		t.Errorf("sub-resource without auth: status = %d reached = %v, want 401/false", resp3.StatusCode, reached)
	}
}

func TestViewProxyNilValidatorRefuses(t *testing.T) {
	uURL, _ := url.Parse("http://127.0.0.1:1")
	vp := NewViewProxy(uURL, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?token=anything", nil)
	vp.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("nil validator must refuse: status = %d", rec.Code)
	}
}
