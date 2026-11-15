package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChainAndRedaction(t *testing.T) {
	dir := t.TempDir()
	red := NewRedactor()
	red.AddSecret("correct horse battery")
	l, err := OpenFile(filepath.Join(dir, "audit.jsonl"), red)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = l.Append(ctx, Entry{Actor: "test", Kind: "action", Payload: map[string]any{
		"password":      "correct horse battery",
		"note":          "typed correct horse battery into field",
		"authorization": "Bearer abc.def.ghi",
		"status_code":   200,
		"header":        "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig",
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.Append(ctx, Entry{Actor: "test", Kind: "navigate", Payload: map[string]any{"url": "http://x/login"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	s := string(raw)
	for _, bad := range []string{"correct horse battery", "abc.def.ghi", "eyJhbGciOiJIUzI1NiJ9"} {
		if strings.Contains(s, bad) {
			t.Fatalf("secret %q leaked into audit log:\n%s", bad, s)
		}
	}
	if !strings.Contains(s, `"status_code":200`) {
		t.Fatalf("safe key was wrongly redacted:\n%s", s)
	}

	// Reopen: chain continues and verifies.
	l2, err := OpenFile(filepath.Join(dir, "audit.jsonl"), red)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if _, err := l2.Append(ctx, Entry{Actor: "t", Kind: "k"}); err != nil {
		t.Fatal(err)
	}
	entries, err := l2.Entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[2].Seq != 3 || entries[2].PrevHash != entries[1].Hash {
		t.Fatalf("chain not continued: %+v", entries)
	}
	if broken, err := l2.Verify(ctx); err != nil || broken != 0 {
		t.Fatalf("verify: broken=%d err=%v", broken, err)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.jsonl")
	l, _ := OpenFile(p, nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := l.Append(ctx, Entry{Actor: "a", Kind: "k", Payload: map[string]any{"i": i}}); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	raw, _ := os.ReadFile(p)
	tampered := strings.Replace(string(raw), `"i":1`, `"i":9`, 1)
	os.WriteFile(p, []byte(tampered), 0o600)
	l2, _ := OpenFile(p, nil)
	defer l2.Close()
	broken, err := l2.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if broken != 2 {
		t.Fatalf("want broken seq 2, got %d", broken)
	}
}
