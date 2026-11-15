package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
)

func testKey(t *testing.T) *[32]byte {
	t.Helper()
	k, err := NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(KeyEnv, k)
	key, err := KeyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestStoreRoundTripAndEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	s, err := NewFileStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	creds := Credentials{UsernameOrEmail: "alice@example.com", Password: "correct horse", TOTPSeed: "JBSWY3DPEHPK3PXP"}
	if err := s.PutCredentials(ctx, "http://lab/simple", "alice", creds); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProfile(ctx, "alice", Profile{Fields: map[string]string{"name": "Alice"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCredentials(ctx, "alice")
	if err != nil || got != creds {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	p, _ := s.GetProfile(ctx, "alice")
	if p.Fields["name"] != "Alice" {
		t.Fatalf("profile lost: %+v", p)
	}
	tg, _ := s.Target(ctx, "alice")
	if tg != "http://lab/simple" {
		t.Fatalf("target: %q", tg)
	}
	// File must not contain plaintext.
	raw, _ := os.ReadFile(s.path("alice"))
	if strings.Contains(string(raw), "correct horse") || strings.Contains(string(raw), "alice@example.com") {
		t.Fatal("plaintext found in creds file")
	}
	// Wrong key fails.
	var other [32]byte
	other[0] = 1
	if _, err := Decrypt(&other, raw); err == nil {
		t.Fatal("decrypt with wrong key should fail")
	}
	if _, err := s.GetCredentials(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	list, _ := s.List(ctx, "")
	if len(list) != 1 || list[0].Creds.Password != "" {
		t.Fatalf("list should hide secrets: %+v", list)
	}
}

func TestKeyFromEnvValidation(t *testing.T) {
	t.Setenv(KeyEnv, base64.StdEncoding.EncodeToString([]byte("short")))
	if _, err := KeyFromEnv(); err == nil {
		t.Fatal("short key accepted")
	}
	t.Setenv(KeyEnv, "")
	if _, err := KeyFromEnv(); err == nil {
		t.Fatal("empty key accepted")
	}
}
