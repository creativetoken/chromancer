package lab

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error { return nil }}
}

func TestLabAppsExpectAndCookieLogin(t *testing.T) {
	s := New()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	s.SetBase(ts.URL)

	for _, app := range []string{"simple", "multistep", "modal", "iframe", "spa-jwt", "hidden-entry", "bound-session"} {
		resp, err := http.Get(ts.URL + "/" + app + "/__lab/expect")
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s expect: %v %v", app, err, resp)
		}
		var e Expect
		_ = json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		if e.App != app || e.LandingURL == "" || e.MeURL == "" || !strings.HasPrefix(e.Root, ts.URL) {
			t.Fatalf("%s bad expect: %+v", app, e)
		}
	}

	// simple: form login sets cookie and /api/me works.
	c := newClient(t)
	resp, err := c.PostForm(ts.URL+"/simple/login", url.Values{"email": {DefaultUser}, "password": {DefaultPass}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.HasSuffix(resp.Request.URL.Path, "/simple/app") {
		t.Fatalf("expected redirect to app, got %s", resp.Request.URL)
	}
	me, _ := c.Get(ts.URL + "/simple/api/me")
	if me.StatusCode != 200 {
		t.Fatalf("me status %d", me.StatusCode)
	}
	me.Body.Close()
	// wrong password rejected
	bad, _ := c.PostForm(ts.URL+"/simple/login", url.Values{"email": {DefaultUser}, "password": {"nope"}})
	if bad.StatusCode != 401 {
		t.Fatalf("bad creds status %d", bad.StatusCode)
	}
	bad.Body.Close()
}

func TestSPAJWTAndBoundSession(t *testing.T) {
	s := New()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// spa-jwt bearer
	resp, err := http.Post(ts.URL+"/spa-jwt/api/login", "application/json", strings.NewReader(`{"email":"alice@example.com","password":"correct horse"}`))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if !strings.HasPrefix(tok.Token, "eyJ") {
		t.Fatalf("no jwt: %q", tok.Token)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/spa-jwt/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	me, _ := http.DefaultClient.Do(req)
	if me.StatusCode != 200 {
		t.Fatalf("bearer me %d", me.StatusCode)
	}
	me.Body.Close()
	noauth, _ := http.Get(ts.URL + "/spa-jwt/api/me")
	if noauth.StatusCode != 401 {
		t.Fatalf("expected 401 without bearer, got %d", noauth.StatusCode)
	}
	noauth.Body.Close()

	// bound-session: cookie alone is not enough; a nonce cannot be reused.
	c := newClient(t)
	r, _ := c.PostForm(ts.URL+"/bound-session/login", url.Values{"email": {DefaultUser}, "password": {DefaultPass}})
	r.Body.Close()
	m1, _ := c.Get(ts.URL + "/bound-session/api/me")
	body, _ := io.ReadAll(m1.Body)
	m1.Body.Close()
	if m1.StatusCode != 401 || !strings.Contains(string(body), "signature") {
		t.Fatalf("expected signature error, got %d %s", m1.StatusCode, body)
	}
}
