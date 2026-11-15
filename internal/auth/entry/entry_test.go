package entry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheRoundTripAndInvalidate(t *testing.T) {
	dir := t.TempDir()
	r := &Resolver{CacheDir: dir}
	if r.readCache("http://app", Login) != nil {
		t.Fatal("empty cache should be nil")
	}
	ep := Entrypoint{URL: "http://app/login", How: "path", Variant: "form", Reached: time.Now()}
	r.writeCache("http://app", Login, ep)
	got := r.readCache("http://app", Login)
	if got == nil || got.URL != ep.URL {
		t.Fatalf("cache roundtrip: %+v", got)
	}
	// Signup slot independent.
	if r.readCache("http://app", Signup) != nil {
		t.Fatal("signup slot should be empty")
	}
	r.writeCache("http://app", Signup, Entrypoint{URL: "http://app/signup", How: "path"})
	if r.readCache("http://app", Login) == nil || r.readCache("http://app", Signup) == nil {
		t.Fatal("both slots should be set")
	}
	r.Invalidate("http://app", Login)
	if r.readCache("http://app", Login) != nil || r.readCache("http://app", Signup) == nil {
		t.Fatal("invalidate should clear only login")
	}
	// File exists and is private.
	if st, err := os.Stat(r.cachePath("http://app")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("cache file perms: %v %v", st, err)
	}
	_ = filepath.Base(r.cachePath("http://app"))
}

func TestCommonPaths(t *testing.T) {
	if commonPaths(Login)[0] != "/login" || commonPaths(Signup)[0] != "/signup" {
		t.Fatal("common paths")
	}
}
