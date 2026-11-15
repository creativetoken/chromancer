package model

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`: `{"a":1}`,
		"Sure! ```json\n{\"handle\": 3}\n``` done":         `{"handle": 3}`,
		`prose before {"x":{"y":"}"}} prose after`:         `{"x":{"y":"}"}}`,
		"```\n{\"k\":\"v\"}\n```":                          `{"k":"v"}`,
		`text {"a":"escaped \" quote","b":[1,2]} trailing`: `{"a":"escaped \" quote","b":[1,2]}`,
	}
	for in, want := range cases {
		got, err := ExtractJSON(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if string(got) != want {
			t.Fatalf("%q: got %s want %s", in, got, want)
		}
	}
	if _, err := ExtractJSON("no json here"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := ExtractJSON(`{"broken": }`); err == nil {
		t.Fatal("expected invalid json error")
	}
}

func TestMockStructuredAndRouter(t *testing.T) {
	m := NewMock(func(prompt string) string {
		if strings.Contains(prompt, "pick") {
			return `the answer is {"handle": 7}`
		}
		return "{}"
	})
	r := &StaticRouter{Default: m}
	p := r.For(TaskClassify)
	raw, usage, err := p.Structured(context.Background(), []Message{{Role: User, Text: "pick one"}}, json.RawMessage(`{}`), ChatOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Handle int }
	if err := json.Unmarshal(raw, &out); err != nil || out.Handle != 7 {
		t.Fatalf("got %s err %v", raw, err)
	}
	if usage == nil || m.CallCount() != 1 {
		t.Fatalf("usage/calls wrong: %v %d", usage, m.CallCount())
	}
}
