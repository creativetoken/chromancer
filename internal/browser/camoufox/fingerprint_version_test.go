package camoufox

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// sameJSON reports whether two JSON blobs are semantically equal (ignoring
// insignificant whitespace, which MarshalIndent introduces).
func sameJSON(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return ca.String() == cb.String()
}

func TestCheckPlaywrightVersion(t *testing.T) {
	// Same major.minor (patch differs) is fine.
	if err := CheckPlaywrightVersion(PlaywrightVersion); err != nil {
		t.Errorf("exact match should pass: %v", err)
	}
	mm, _ := majorMinor(PlaywrightVersion)
	if err := CheckPlaywrightVersion(mm + ".99"); err != nil {
		t.Errorf("patch difference should pass: %v", err)
	}
	// Minor/major differences must fail.
	for _, bad := range []string{"1.51.0", "2.52.0", "1.53.0", "not-a-version", ""} {
		if err := CheckPlaywrightVersion(bad); err == nil {
			t.Errorf("CheckPlaywrightVersion(%q) should fail", bad)
		}
	}
}

func TestResolveFingerprintStability(t *testing.T) {
	dir := t.TempDir()

	// First run: nothing saved → regenerate.
	fp, regen, err := ResolveFingerprint(dir, "gb", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !regen || fp != nil {
		t.Fatalf("first run should regenerate (regen=%v fp=%v)", regen, fp)
	}

	// Persist a fingerprint for region gb.
	saved := &Fingerprint{Region: "gb", Config: json.RawMessage(`{"ua":"x","tz":"Europe/London"}`), CreatedAt: time.Now()}
	if err := SaveFingerprint(dir, saved); err != nil {
		t.Fatal(err)
	}

	// Same region → reuse the SAME fingerprint, no regenerate (stable across runs).
	got, regen, err := ResolveFingerprint(dir, "gb", nil)
	if err != nil {
		t.Fatal(err)
	}
	if regen {
		t.Error("same-region run must reuse the saved fingerprint, not regenerate")
	}
	if got == nil || !sameJSON(got.Config, saved.Config) {
		t.Errorf("reused fingerprint differs: %+v", got)
	}

	// Different region → regenerate with a warning.
	var warned bool
	_, regen, err = ResolveFingerprint(dir, "us", func(string, ...any) { warned = true })
	if err != nil {
		t.Fatal(err)
	}
	if !regen || !warned {
		t.Errorf("region change should regenerate with a warning (regen=%v warned=%v)", regen, warned)
	}
}

func TestFingerprintDiffersAcrossAccounts(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := SaveFingerprint(a, &Fingerprint{Region: "gb", Config: json.RawMessage(`{"id":"A"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFingerprint(b, &Fingerprint{Region: "gb", Config: json.RawMessage(`{"id":"B"}`)}); err != nil {
		t.Fatal(err)
	}
	fa, _ := LoadFingerprint(a)
	fb, _ := LoadFingerprint(b)
	if fa == nil || fb == nil || string(fa.Config) == string(fb.Config) {
		t.Errorf("two accounts must keep distinct fingerprints: %v vs %v", fa, fb)
	}
}
