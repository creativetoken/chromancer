package verify

import (
	"context"
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B test vectors. The seeds are ASCII keys; the spec's SHA256
// and SHA512 vectors use longer keys ("...3132" repetitions).
func b32(ascii string) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(ascii))
}

func TestRFC6238Vectors(t *testing.T) {
	sha1Key := b32("12345678901234567890")                                               // 20 bytes
	sha256Key := b32("12345678901234567890123456789012")                                 // 32 bytes
	sha512Key := b32("1234567890123456789012345678901234567890123456789012345678901234") // 64 bytes

	cases := []struct {
		t    int64
		alg  Algorithm
		key  string
		want string // 8-digit expected value
	}{
		{59, AlgSHA1, sha1Key, "94287082"},
		{1111111109, AlgSHA1, sha1Key, "07081804"},
		{1234567890, AlgSHA1, sha1Key, "89005924"},
		{2000000000, AlgSHA1, sha1Key, "69279037"},
		{59, AlgSHA256, sha256Key, "46119246"},
		{1111111109, AlgSHA256, sha256Key, "68084774"},
		{59, AlgSHA512, sha512Key, "90693936"},
		{1111111109, AlgSHA512, sha512Key, "25091201"},
	}
	for _, c := range cases {
		sd := Seed{Secret: c.key, Algorithm: c.alg, Digits: 8, Period: 30 * time.Second}
		got, err := sd.CodeAt(time.Unix(c.t, 0).UTC())
		if err != nil {
			t.Fatalf("%d/%s: %v", c.t, c.alg, err)
		}
		if got != c.want {
			t.Errorf("%d/%s: got %s want %s", c.t, c.alg, got, c.want)
		}
	}

	// 6-digit and 60s period also work (truncation + window math).
	sd := Seed{Secret: sha1Key, Algorithm: AlgSHA1, Digits: 6, Period: 60 * time.Second}
	if got, _ := sd.CodeAt(time.Unix(59, 0)); len(got) != 6 {
		t.Fatalf("6-digit length: %q", got)
	}
}

func TestParseSeed(t *testing.T) {
	// Raw base32.
	sd, err := ParseSeed("JBSWY3DPEHPK3PXP")
	if err != nil || sd.Algorithm != AlgSHA1 || sd.Digits != 6 || sd.Period != 30*time.Second {
		t.Fatalf("raw: %+v %v", sd, err)
	}
	// Full otpauth URI with options.
	sd, err = ParseSeed("otpauth://totp/Acme:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Acme&algorithm=SHA256&digits=8&period=60")
	if err != nil {
		t.Fatal(err)
	}
	if sd.Issuer != "Acme" || sd.Account != "alice@example.com" || sd.Algorithm != AlgSHA256 || sd.Digits != 8 || sd.Period != 60*time.Second {
		t.Fatalf("otpauth parse: %+v", sd)
	}
	// Malformed URIs.
	for _, bad := range []string{"otpauth://hotp/x?secret=JBSWY3DPEHPK3PXP", "otpauth://totp/x?secret=not-base32-!!", "otpauth://totp/x?secret=JBSWY3DPEHPK3PXP&digits=7", "", "   "} {
		if _, err := ParseSeed(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestPlannedCounterBoundaryGuard(t *testing.T) {
	period := 30 * time.Second
	// 20s into a window: plenty of time, use the current window, no wait.
	now := time.Unix(1000*30+20, 0)
	c, issueAt := plannedCounter(now, period, 3*time.Second)
	if c != 1000 || !issueAt.Equal(now) {
		t.Fatalf("mid-window: c=%d issueAt=%v", c, issueAt)
	}
	// 1s left: roll to the next window and wait for it.
	now = time.Unix(1000*30+29, 0)
	c, issueAt = plannedCounter(now, period, 3*time.Second)
	if c != 1001 || !issueAt.Equal(time.Unix(1001*30, 0)) {
		t.Fatalf("boundary: c=%d issueAt=%v", c, issueAt)
	}
}

func TestTOTPSkewRetry(t *testing.T) {
	// Fixed clock at the start of a window; no waiting.
	fixed := time.Unix(1000*30+5, 0)
	var slept time.Duration
	p := &TOTPProvider{
		SeedForTarget: func(_, _ string) string { return "JBSWY3DPEHPK3PXP" },
		Now:           func() time.Time { return fixed },
		Sleep:         func(_ context.Context, d time.Duration) { slept += d },
	}
	req := Request{Channel: TOTP, Account: "a", Target: "app"}
	first, err := p.GetCode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// Second call in the same window = previous rejected: next window's code.
	second, err := p.GetCode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	wantSecond, _ := codeForCounter("JBSWY3DPEHPK3PXP", 1001, 6, AlgSHA1)
	if second.Code != wantSecond || second.Code == first.Code {
		t.Fatalf("skew retry: first=%s second=%s wantSecond=%s", first.Code, second.Code, wantSecond)
	}
	// Third call gives up rather than brute-forcing further windows.
	if _, err := p.GetCode(context.Background(), req); err != ErrDeclined {
		t.Fatalf("third call err=%v, want ErrDeclined", err)
	}
	if slept != 0 {
		t.Fatalf("should not have waited mid-window, slept %v", slept)
	}
}

func TestCurrentCodeTool(t *testing.T) {
	fixed := time.Unix(1000*30+25, 0)
	p := &TOTPProvider{SeedForTarget: func(_, _ string) string { return "JBSWY3DPEHPK3PXP" }, Now: func() time.Time { return fixed }}
	code, left, err := p.CurrentCode("a", "app")
	if err != nil || len(code) != 6 {
		t.Fatalf("code=%q left=%d err=%v", code, left, err)
	}
	if left != 5 {
		t.Fatalf("seconds left = %d, want 5", left)
	}
	_ = strings.TrimSpace(code)
}
