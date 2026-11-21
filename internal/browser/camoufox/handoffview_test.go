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
