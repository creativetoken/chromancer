package perceive

import "testing"

func TestPathTemplate(t *testing.T) {
	cases := map[string]string{
		"http://h/items/123":                 "h/items/{id}",
		"http://h/items/123/edit?x=1":        "h/items/{id}/edit",
		"http://h/u/550e8400-e29b-41d4-a716": "h/u/{id}",
		"http://h/login":                     "h/login",
		"http://h/a/1/b/2":                   "h/a/{id}/b/{id}",
	}
	for in, want := range cases {
		if got := PathTemplate(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestHashIgnoresIDsAndOrder(t *testing.T) {
	a := State{URL: "http://h/items/1", Nodes: []Node{{Handle: 1, Tag: "a", Name: "Home", Visible: true}, {Handle: 2, Tag: "button", Name: "Buy", Visible: true}}}
	b := State{URL: "http://h/items/2", Nodes: []Node{{Handle: 5, Tag: "button", Name: "Buy", Visible: true}, {Handle: 9, Tag: "a", Name: "Home", Visible: true}}}
	if Hash(a) != Hash(b) {
		t.Fatal("parameterised routes with same structure should hash equal")
	}
	c := State{URL: "http://h/items/2", Nodes: []Node{{Handle: 5, Tag: "button", Name: "Delete", Visible: true}}}
	if Hash(a) == Hash(c) {
		t.Fatal("different structure should hash differently")
	}
}

func TestCompactAndFind(t *testing.T) {
	s := State{URL: "u", Title: "t", Nodes: []Node{{Handle: 3, Tag: "input", Type: "email", Name: "Email", Visible: true}}}
	out := Compact(s)
	if !contains(out, `#3 input[email] name="Email"`) {
		t.Fatalf("compact: %s", out)
	}
	if n, ok := s.Find(3); !ok || n.Type != "email" {
		t.Fatal("find failed")
	}
	if Selector(3) != `[data-ab-handle="3"]` {
		t.Fatal("selector")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
