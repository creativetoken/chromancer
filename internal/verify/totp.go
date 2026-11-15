package verify

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SeedFunc returns the base32 TOTP seed for an account ("" if none).
type SeedFunc func(account string) string

// Algorithm is the HMAC hash a TOTP seed uses.
type Algorithm string

// Algorithms (RFC 6238).
const (
	AlgSHA1   Algorithm = "SHA1"
	AlgSHA256 Algorithm = "SHA256"
	AlgSHA512 Algorithm = "SHA512"
)

// Seed is a parsed TOTP configuration.
type Seed struct {
	Secret    string // base32, upper-case, no padding
	Issuer    string
	Account   string
	Algorithm Algorithm
	Digits    int
	Period    time.Duration
}

func (s Seed) withDefaults() Seed {
	if s.Algorithm == "" {
		s.Algorithm = AlgSHA1
	}
	if s.Digits == 0 {
		s.Digits = 6
	}
	if s.Period == 0 {
		s.Period = 30 * time.Second
	}
	return s
}

// ParseSeed accepts a raw base32 secret or a full otpauth://totp/... URI.
func ParseSeed(s string) (Seed, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Seed{}, fmt.Errorf("totp: empty seed")
	}
	if !strings.HasPrefix(strings.ToLower(s), "otpauth://") {
		sec, err := normalizeSecret(s)
		if err != nil {
			return Seed{}, err
		}
		return Seed{Secret: sec}.withDefaults(), nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return Seed{}, fmt.Errorf("totp: bad otpauth uri: %w", err)
	}
	if strings.ToLower(u.Host) != "totp" {
		return Seed{}, fmt.Errorf("totp: not a totp uri (type %q)", u.Host)
	}
	q := u.Query()
	sec, err := normalizeSecret(q.Get("secret"))
	if err != nil {
		return Seed{}, err
	}
	sd := Seed{Secret: sec, Issuer: q.Get("issuer")}
	label := strings.TrimPrefix(u.Path, "/")
	if i := strings.Index(label, ":"); i >= 0 {
		if sd.Issuer == "" {
			sd.Issuer = label[:i]
		}
		sd.Account = label[i+1:]
	} else {
		sd.Account = label
	}
	switch strings.ToUpper(q.Get("algorithm")) {
	case "SHA256":
		sd.Algorithm = AlgSHA256
	case "SHA512":
		sd.Algorithm = AlgSHA512
	case "SHA1", "":
		sd.Algorithm = AlgSHA1
	default:
		return Seed{}, fmt.Errorf("totp: unknown algorithm %q", q.Get("algorithm"))
	}
	if d := q.Get("digits"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || (n != 6 && n != 8) {
			return Seed{}, fmt.Errorf("totp: bad digits %q", d)
		}
		sd.Digits = n
	}
	if p := q.Get("period"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 {
			return Seed{}, fmt.Errorf("totp: bad period %q", p)
		}
		sd.Period = time.Duration(n) * time.Second
	}
	return sd.withDefaults(), nil
}

func normalizeSecret(s string) (string, error) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	if s == "" {
		return "", fmt.Errorf("totp: missing secret")
	}
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s); err != nil {
		return "", fmt.Errorf("totp: secret is not valid base32: %w", err)
	}
	return s, nil
}

func newHash(a Algorithm) func() hash.Hash {
	switch a {
	case AlgSHA256:
		return sha256.New
	case AlgSHA512:
		return sha512.New
	default:
		return sha1.New
	}
}

// codeForCounter computes an HOTP value for a counter.
func codeForCounter(secretB32 string, counter uint64, digits int, alg Algorithm) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.ReplaceAll(secretB32, " ", "")))
	if err != nil {
		return "", fmt.Errorf("totp: bad seed: %w", err)
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(newHash(alg), key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[offset])&0x7f)<<24 | uint32(sum[offset+1])<<16 | uint32(sum[offset+2])<<8 | uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod), nil
}

// CodeAt returns the seed's code at time t.
func (s Seed) CodeAt(t time.Time) (string, error) {
	sd := s.withDefaults()
	counter := uint64(t.Unix() / int64(sd.Period.Seconds()))
	return codeForCounter(sd.Secret, counter, sd.Digits, sd.Algorithm)
}

// Code computes an RFC 6238 SHA1 TOTP for a base32 seed at time t (kept for
// callers that only need the simple case).
func Code(seedB32 string, t time.Time, digits int, period time.Duration) (string, error) {
	return Seed{Secret: strings.ToUpper(strings.ReplaceAll(seedB32, " ", "")), Digits: digits, Period: period}.CodeAt(t)
}

// plannedCounter returns the counter to use and when to issue it: if fewer than
// `guard` remain in the current window, it rolls to the next window so the typed
// code does not expire before submission.
func plannedCounter(now time.Time, period, guard time.Duration) (counter uint64, issueAt time.Time) {
	p := int64(period.Seconds())
	cur := now.Unix() / p
	remaining := time.Duration((cur+1)*p-now.Unix()) * time.Second
	if remaining < guard {
		return uint64(cur + 1), time.Unix((cur+1)*p, 0)
	}
	return uint64(cur), now
}

// TOTPProvider computes time-based codes locally from a stored seed, per
// (account, target). It guards the period boundary and, when asked again inside
// the same window (the previous code was rejected), returns the next window's
// code once.
type TOTPProvider struct {
	// SeedForTarget returns the seed string (base32 or otpauth URI) for an
	// (account, target), or "" if none.
	SeedForTarget func(account, target string) string
	// Seed is the legacy per-account lookup, used when SeedForTarget is nil.
	Seed SeedFunc
	// Digits / Period override only the legacy Seed path.
	Digits int
	Period time.Duration
	// Now and Sleep are injectable for tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
	Guard time.Duration // boundary guard (default 3s)

	mu     sync.Mutex
	issued map[string]issueState
}

type issueState struct {
	counter uint64
	tries   int
}

// NewTOTP builds a TOTPProvider from a per-account base32 seed (legacy).
func NewTOTP(seed SeedFunc) *TOTPProvider {
	return &TOTPProvider{Seed: seed, Digits: 6, Period: 30 * time.Second}
}

func (p *TOTPProvider) resolve(account, target string) (Seed, bool) {
	raw := ""
	if p.SeedForTarget != nil {
		raw = p.SeedForTarget(account, target)
	}
	if raw == "" && p.Seed != nil {
		raw = p.Seed(account)
		if raw != "" {
			sd, err := ParseSeed(raw)
			if err != nil {
				return Seed{}, false
			}
			if p.Digits != 0 {
				sd.Digits = p.Digits
			}
			if p.Period != 0 {
				sd.Period = p.Period
			}
			return sd.withDefaults(), true
		}
	}
	if raw == "" {
		return Seed{}, false
	}
	sd, err := ParseSeed(raw)
	if err != nil {
		return Seed{}, false
	}
	return sd, true
}

// Supports implements Provider: TOTP, and only if a seed exists for the account.
// (Per-target seeds are resolved with an empty target here; wiring makes the
// account's seed resolvable that way.)
func (p *TOTPProvider) Supports(ch Channel, account string) bool {
	if ch != TOTP {
		return false
	}
	_, ok := p.resolve(account, "")
	return ok
}

func (p *TOTPProvider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *TOTPProvider) sleep(ctx context.Context, d time.Duration) {
	if p.Sleep != nil {
		p.Sleep(ctx, d)
		return
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// GetCode returns the current code for (account, target), guarded against the
// period boundary, advancing one window on a same-window retry.
func (p *TOTPProvider) GetCode(ctx context.Context, req Request) (Result, error) {
	sd, ok := p.resolve(req.Account, req.Target)
	if !ok {
		return Result{}, ErrUnsupported
	}
	guard := p.Guard
	if guard == 0 {
		guard = 3 * time.Second
	}
	counter, issueAt := plannedCounter(p.now(), sd.Period, guard)
	if wait := issueAt.Sub(p.now()); wait > 0 {
		p.sleep(ctx, wait)
		if ctx.Err() != nil {
			return Result{}, ErrTimeout
		}
	}

	key := req.Account + "|" + req.Target
	p.mu.Lock()
	if p.issued == nil {
		p.issued = map[string]issueState{}
	}
	st := p.issued[key]
	if st.counter == counter {
		st.tries++
	} else {
		st = issueState{counter: counter, tries: 0}
	}
	p.issued[key] = st
	tries := st.tries
	p.mu.Unlock()

	if tries >= 2 {
		return Result{}, ErrDeclined // never brute-force more than one extra window
	}
	use := counter + uint64(tries) // tries 0 -> current, tries 1 -> next window
	code, err := codeForCounter(sd.Secret, use, sd.Digits, sd.Algorithm)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: ResCode, Code: code}, nil
}

// Resend is a no-op for TOTP.
func (p *TOTPProvider) Resend(context.Context, Request) error { return nil }

// CurrentCode returns the code and seconds remaining in the window (for the
// get_totp tool). It does not consume a retry.
func (p *TOTPProvider) CurrentCode(account, target string) (code string, secondsLeft int, err error) {
	sd, ok := p.resolve(account, target)
	if !ok {
		return "", 0, ErrUnsupported
	}
	now := p.now()
	code, err = sd.CodeAt(now)
	if err != nil {
		return "", 0, err
	}
	period := int64(sd.Period.Seconds())
	secondsLeft = int(period - now.Unix()%period)
	return code, secondsLeft, nil
}
