package explore

import (
	"testing"

	"github.com/creativetoken/chromancer/internal/perceive"
)

func TestDenyReason(t *testing.T) {
	deny := []struct{ label, href string }{
		{"Log out", "/logout"},
		{"Sign Out", ""},
		{"Delete my account", "/account/delete"},
		{"Change password", "/settings/password"},
		{"Revoke all sessions", ""},
		{"", "/user/logout"},
		{"Manage API keys", ""},
	}
	for _, d := range deny {
		if DenyReason(d.label, d.href) == "" {
			t.Errorf("should deny %q / %q", d.label, d.href)
		}
	}
	allow := []struct{ label, href string }{
		{"Home", "/home"},
		{"View item", "/item/1"},
		{"Settings", "/settings"},
		{"Next page", "/items?page=2"},
		{"Save profile", "/profile"},
	}
	for _, a := range allow {
		if r := DenyReason(a.label, a.href); r != "" {
			t.Errorf("should allow %q / %q (got %q)", a.label, a.href, r)
		}
	}
}

func TestBasePrefixAndResolve(t *testing.T) {
	if got := basePrefix("http://h:8089/crawl/app?x=1"); got != "http://h:8089/crawl/" {
		t.Fatalf("basePrefix: %q", got)
	}
	if got := resolveURL("http://h/crawl/app", "item/3"); got != "http://h/crawl/item/3" {
		t.Fatalf("resolve relative: %q", got)
	}
	if got := resolveURL("http://h/crawl/app", "/crawl/settings#top"); got != "http://h/crawl/settings" {
		t.Fatalf("resolve absolute+frag: %q", got)
	}
}

func TestSampleValueTypes(t *testing.T) {
	cases := []struct {
		n    perceive.Node
		want string
	}{
		{perceive.Node{Type: "email"}, "alex.taylor@example.com"},
		{perceive.Node{Name: "Search"}, "widget"},
		{perceive.Node{Name: "Full name", Type: "text"}, "Alex Taylor"},
		{perceive.Node{Tag: "textarea", Name: "Bio"}, "Updated by the automated crawler."},
	}
	for _, c := range cases {
		if got := sampleValue(c.n); got != c.want {
			t.Errorf("sampleValue(%+v)=%q want %q", c.n, got, c.want)
		}
	}
}
