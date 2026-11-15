package verify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestXOAUTH2Response(t *testing.T) {
	got := XOAUTH2Response("alice@gmail.com", "ya29.TOKEN")
	want := "user=alice@gmail.com\x01auth=Bearer ya29.TOKEN\x01\x01"
	if got != want {
		t.Fatalf("XOAUTH2 response = %q, want %q", got, want)
	}
	// The SASL client reports the XOAUTH2 mechanism and this initial response.
	mech, ir, err := NewXOAUTH2("alice@gmail.com", "ya29.TOKEN").Start()
	if err != nil || mech != "XOAUTH2" || string(ir) != want {
		t.Fatalf("Start = (%q, %q, %v)", mech, ir, err)
	}
}

func TestAccessTokenRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "rt-good" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ya29.fresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()
	cfg := OAuthConfig{ClientID: "cid", ClientSecret: "csec", TokenURL: srv.URL}

	tok, err := cfg.AccessToken(context.Background(), "rt-good")
	if err != nil || tok != "ya29.fresh" {
		t.Fatalf("access token = %q err=%v", tok, err)
	}
}

func TestAccessTokenInvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	cfg := OAuthConfig{ClientID: "cid", ClientSecret: "csec", TokenURL: srv.URL}

	_, err := cfg.AccessToken(context.Background(), "rt-revoked")
	if err != ErrReauthRequired {
		t.Fatalf("err = %v, want ErrReauthRequired", err)
	}
	if !strings.Contains(ErrReauthRequired.Error(), "mailbox login") {
		t.Fatalf("reauth message should mention mailbox login: %v", ErrReauthRequired)
	}
}
