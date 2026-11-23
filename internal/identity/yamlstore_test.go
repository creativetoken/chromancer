package identity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestYAML(t *testing.T) *YAMLStore {
	t.Helper()
	s, err := NewYAMLStore(filepath.Join(t.TempDir(), "accounts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestYAMLStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestYAML(t)

	want := Credentials{UsernameOrEmail: "u@example.com", Password: "pw", TOTPSeed: "SEED", Phone: "+15551234"}
	if err := s.PutCredentials(ctx, "https://x.test", "acct", want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCredentials(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("creds round-trip: got %+v want %+v", got, want)
	}
	if tgt, err := s.Target(ctx, "acct"); err != nil || tgt != "https://x.test" {
		t.Fatalf("target: %q %v", tgt, err)
	}
}

func TestYAMLStoreMissingAccount(t *testing.T) {
	s := newTestYAML(t)
	if _, err := s.GetCredentials(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestYAMLStoreEnvInterpolation(t *testing.T) {
	ctx := context.Background()
	s := newTestYAML(t)
	// Write a file that references an env var; Get must resolve it, but the file
	// must KEEP the ${ENV} reference (not bake in the resolved secret).
	if err := os.WriteFile(s.path, []byte(
		"accounts:\n  a:\n    target: https://x.test\n    username: literal@example.com\n    password: ${TEST_PW}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PW", "s3cret")

	got, err := s.GetCredentials(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Password != "s3cret" {
		t.Fatalf("env not resolved: %q", got.Password)
	}
	if got.UsernameOrEmail != "literal@example.com" {
		t.Fatalf("literal mangled: %q", got.UsernameOrEmail)
	}
	// A PutProfile round-trip must preserve the ${TEST_PW} reference on disk.
	if err := s.PutProfile(ctx, "a", Profile{Fields: map[string]string{"name": "n"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.path)
	if !strings.Contains(string(raw), "${TEST_PW}") {
		t.Fatalf("env reference was baked out:\n%s", raw)
	}
}

func TestYAMLStoreListOmitsSecrets(t *testing.T) {
	ctx := context.Background()
	s := newTestYAML(t)
	_ = s.PutCredentials(ctx, "https://a.test", "a", Credentials{UsernameOrEmail: "ua", Password: "pa"})
	_ = s.PutCredentials(ctx, "https://b.test", "b", Credentials{UsernameOrEmail: "ub", Password: "pb"})

	all, err := s.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(all))
	}
	for _, a := range all {
		if a.Creds.Password != "" || a.Creds.TOTPSeed != "" {
			t.Fatalf("List leaked a secret for %s", a.ID)
		}
	}
	only, err := s.List(ctx, "https://b.test")
	if err != nil || len(only) != 1 || only[0].ID != "b" {
		t.Fatalf("target filter: %+v %v", only, err)
	}
}

func TestEncryptNilKeyPlaintextRoundTrip(t *testing.T) {
	plain := []byte(`{"hello":"world"}`)
	enc, err := Encrypt(nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(enc), "PLN1") {
		t.Fatalf("plaintext payload missing PLN1 marker: %q", enc)
	}
	got, err := Decrypt(nil, enc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatalf("plaintext round-trip: got %q want %q", got, plain)
	}
}

func TestEncryptKeyStillWorksAndDecryptsWithNil(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	plain := []byte("secret-bytes")
	enc, err := Encrypt(&key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(enc), "PLN1") {
		t.Fatal("keyed payload must NOT be plaintext")
	}
	// Reading a legacy (keyed) file with no key must fail, not misparse.
	if _, err := Decrypt(nil, enc); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("nil-key decrypt of keyed data: want ErrWrongKey, got %v", err)
	}
	got, err := Decrypt(&key, enc)
	if err != nil || string(got) != string(plain) {
		t.Fatalf("keyed round-trip: %q %v", got, err)
	}
}
