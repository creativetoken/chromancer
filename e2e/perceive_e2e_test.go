package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/browser"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// TestE2EPerceiveLoginForm checks the browser layer + a11y perceiver end to
// end: navigate, enumerate interactive nodes with handles, act on a handle,
// and see the result in the next perception.
func TestE2EPerceiveLoginForm(t *testing.T) {
	h := newHarness(t)
	e := h.expect("simple")
	p, err := h.b.NewPage(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Navigate(h.ctx, e.Root); err != nil {
		t.Fatal(err)
	}
	per := perceive.New()
	st, hash, err := per.Perceive(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" || len(st.Nodes) == 0 {
		t.Fatalf("empty perception: %+v", st)
	}
	var login perceive.Node
	for _, n := range st.Nodes {
		if n.Tag == "a" && strings.EqualFold(n.Name, "Log in") {
			login = n
		}
	}
	if login.Handle == 0 {
		t.Fatalf("login link not perceived:\n%s", perceive.Compact(st))
	}
	el, err := p.Element(h.ctx, nil, perceive.Selector(login.Handle), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Click(h.ctx, el); err != nil {
		t.Fatal(err)
	}
	_ = p.WaitStable(h.ctx, browser.DefaultStableTimeout)
	st2, _, err := per.Perceive(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var sawEmail, sawPassword, sawSubmit bool
	for _, n := range st2.Nodes {
		switch {
		case n.Tag == "input" && n.Type == "email" && strings.EqualFold(n.Name, "Email"):
			sawEmail = true
		case n.Tag == "input" && n.Type == "password":
			sawPassword = true
		case n.Tag == "button" && n.Type == "submit":
			sawSubmit = true
		}
	}
	if !sawEmail || !sawPassword || !sawSubmit {
		t.Fatalf("login form not perceived (email=%v password=%v submit=%v):\n%s", sawEmail, sawPassword, sawSubmit, perceive.Compact(st2))
	}
	if !strings.HasSuffix(st2.URL, "/simple/login") {
		t.Fatalf("expected /simple/login, got %s", st2.URL)
	}

	// Iframe perception: nodes inside the frame are reported with frame index 1.
	if err := p.Navigate(h.ctx, h.expect("iframe").Root); err != nil {
		t.Fatal(err)
	}
	st3, _, err := per.Perceive(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var inFrame bool
	for _, n := range st3.Nodes {
		if n.Frame == 1 && n.Tag == "input" && n.Type == "password" {
			inFrame = true
		}
	}
	if !inFrame {
		t.Fatalf("password field inside iframe not perceived:\n%s", perceive.Compact(st3))
	}
}
