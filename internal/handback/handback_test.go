package handback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
)

func TestRecorderFlagsSecretsAndAuthHeaders(t *testing.T) {
	r := NewRecorder()
	r.rec = AuthRecording{Format: "har-1.2-json", StartedAt: time.Now()}
	// auth phase: login POST with password in body
	r.OnRequest(browser.RequestEvent{ID: "1", Method: "POST", URL: "http://h/login", Type: "Document",
		Headers: map[string]string{"content-type": "application/x-www-form-urlencoded", "cookie": "a=b"}, PostData: "email=a%40b&password=secret"})
	r.OnResponse(browser.ResponseEvent{ID: "1", Status: 303, Headers: map[string]string{"set-cookie": "sid=xyz"}})
	_ = r.MarkAuthenticated(context.Background(), CaptureWindow{N: 2, T: time.Second})
	r.OnRequest(browser.RequestEvent{ID: "2", Method: "GET", URL: "http://h/api/me", Type: "XHR",
		Headers: map[string]string{"authorization": "Bearer tok1", "x-req-nonce": "n1", "accept": "*/*"}})
	r.OnResponse(browser.ResponseEvent{ID: "2", Status: 200, MimeType: "application/json", Body: `{"email":"a@b","token":"t"}`})
	r.OnRequest(browser.RequestEvent{ID: "3", Method: "GET", URL: "http://h/api/me", Type: "Fetch",
		Headers: map[string]string{"authorization": "Bearer tok1", "x-req-nonce": "n2"}})
	r.OnResponse(browser.ResponseEvent{ID: "3", Status: 200, MimeType: "application/json", Body: `{"email":"a@b"}`})
	// Window of N=2 should now be satisfied without waiting for T.
	done := make(chan struct{})
	go func() { r.WaitWindow(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(900 * time.Millisecond):
		t.Fatal("window did not close on N")
	}
	rec, _ := r.Finalize(context.Background())
	if len(rec.Entries) != 3 || rec.Entries[0].Phase != "auth" || rec.Entries[1].Phase != "post_login" {
		t.Fatalf("entries: %+v", rec.Entries)
	}
	if !hasSecret(rec.Entries[0].Request.Headers, "cookie") || !hasSecret(rec.Entries[0].Response.Headers, "set-cookie") {
		t.Fatal("cookie headers not flagged secret")
	}
	if !strings.Contains(strings.Join(rec.Entries[0].SecretFields, ","), "postData.password") {
		t.Fatalf("password field not flagged: %v", rec.Entries[0].SecretFields)
	}
	if !strings.Contains(strings.Join(rec.Entries[1].SecretFields, ","), "body.token") {
		t.Fatalf("token in body not flagged: %v", rec.Entries[1].SecretFields)
	}
	ah := r.AuthHeaders()
	if ah["authorization"] != "Bearer tok1" || ah["x-req-nonce"] == "" {
		t.Fatalf("auth headers: %v", ah)
	}
	if v := r.VaryingHeaders(); len(v) != 1 || v[0] != "x-req-nonce" {
		t.Fatalf("varying: %v", v)
	}
	if len(r.PostLoginXHRs()) != 2 {
		t.Fatal("post-login xhr count")
	}
}

func hasSecret(hs []HARHeader, name string) bool {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) && h.Secret {
			return true
		}
	}
	return false
}

func TestReplayCheckerCookieAndBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			if c, _ := r.Cookie("sid"); c != nil && c.Value == "good" {
				_, _ = w.Write([]byte(`{"email":"a@b","id":1}`))
				return
			}
			if r.Header.Get("Authorization") == "Bearer tok" {
				_, _ = w.Write([]byte(`{"email":"a@b", "id":1}`))
				return
			}
			w.WriteHeader(401)
		case "/api/bound":
			if r.Header.Get("X-Req-Nonce") == "fresh" {
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	rec := func(url string, hdrs map[string]string) AuthRecording {
		var e HAREntry
		e.Phase = "post_login"
		e.Request.Method = "GET"
		e.Request.URL = url
		e.Request.Type = "XHR"
		e.Request.Headers = flagHeaders(hdrs)
		e.Response.Status = 200
		e.Response.MimeType = "application/json"
		e.Response.Body = `{"email":"a@b","id":1}`
		return AuthRecording{Entries: []HAREntry{e}}
	}

	// cookie session replays
	b := SessionBundle{Cookies: []browser.Cookie{{Name: "sid", Value: "good", Path: "/"}}, UserAgent: "ua"}
	ok, reason, err := NewHTTPReplayChecker().Check(ctx, b, rec(srv.URL+"/api/me", map[string]string{"accept": "*/*"}), nil)
	if err != nil || !ok {
		t.Fatalf("cookie replay: ok=%v reason=%s err=%v", ok, reason, err)
	}
	// bearer replays
	b2 := SessionBundle{AuthHeaders: map[string]string{"authorization": "Bearer tok"}, UserAgent: "ua"}
	ok, reason, err = NewHTTPReplayChecker().Check(ctx, b2, rec(srv.URL+"/api/me", nil), nil)
	if err != nil || !ok {
		t.Fatalf("bearer replay: ok=%v reason=%s err=%v", ok, reason, err)
	}
	// bound: stale nonce rejected → reason names the varying header
	e := rec(srv.URL+"/api/bound", map[string]string{"x-req-nonce": "stale"})
	e.Entries[0].Response.Body = `{"ok":true}`
	ok, reason, err = NewHTTPReplayChecker().Check(ctx, SessionBundle{UserAgent: "ua"}, e, []string{"x-req-nonce"})
	if err != nil || ok || !strings.HasPrefix(reason, "js_computed_headers") {
		t.Fatalf("bound replay: ok=%v reason=%s err=%v", ok, reason, err)
	}
	// nothing captured
	ok, reason, _ = NewHTTPReplayChecker().Check(ctx, b, AuthRecording{}, nil)
	if ok || reason != "no_authenticated_xhr_captured" {
		t.Fatalf("empty: %v %s", ok, reason)
	}
}

func TestFileWriterRefs(t *testing.T) {
	dir := t.TempDir()
	w := &FileWriter{Dir: dir, Account: "alice"}
	ctx := context.Background()
	b := SessionBundle{Account: "alice", CapturedAt: time.Now(), Cookies: []browser.Cookie{{Name: "sid", Value: "v"}}}
	ref, err := w.WriteBundle(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.URI, "agentbrowser://artefacts/alice/bundle-") || !strings.HasPrefix(ref.Path, dir) {
		t.Fatalf("ref %+v", ref)
	}
	latest, err := w.LatestBundle(ctx)
	if err != nil || latest.Path != ref.Path {
		t.Fatalf("latest %+v %v", latest, err)
	}
	got, err := w.ReadBundle(ctx, latest)
	if err != nil || got.Cookies[0].Value != "v" {
		t.Fatalf("read back: %+v %v", got, err)
	}
	rref, err := w.WriteRecording(ctx, AuthRecording{StartedAt: time.Now(), Entries: []HAREntry{{Phase: "auth"}}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(rref.Path)
	var back AuthRecording
	if json.Unmarshal(data, &back) != nil || len(back.Entries) != 1 {
		t.Fatal("recording roundtrip")
	}
	if st, _ := os.Stat(ref.Path); st.Mode().Perm() != 0o600 {
		t.Fatalf("artefact perms %v", st.Mode().Perm())
	}
}
