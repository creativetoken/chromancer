package verify

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// SeedFunc returns the base32 TOTP seed for an account ("" if none).
type SeedFunc func(account string) string

// TOTPProvider computes RFC 6238 time-based codes locally from a stored seed.
type TOTPProvider struct {
	Seed   SeedFunc
	Digits int           // default 6
	Period time.Duration // default 30s
}

// NewTOTP builds a TOTPProvider.
func NewTOTP(seed SeedFunc) *TOTPProvider {
	return &TOTPProvider{Seed: seed, Digits: 6, Period: 30 * time.Second}
}

// Supports implements Provider: only TOTP, and only if a seed exists.
func (p *TOTPProvider) Supports(ch Channel, account string) bool {
	return ch == TOTP && p.Seed != nil && p.Seed(account) != ""
}

// GetCode implements Provider: returns the current code immediately.
func (p *TOTPProvider) GetCode(_ context.Context, req Request) (Result, error) {
	seed := ""
	if p.Seed != nil {
		seed = p.Seed(req.Account)
	}
	if seed == "" {
		return Result{}, ErrUnsupported
	}
	code, err := Code(seed, time.Now(), p.digits(), p.period())
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: ResCode, Code: code}, nil
}

// Resend is a no-op for TOTP (the next code is just the current one).
func (p *TOTPProvider) Resend(context.Context, Request) error { return nil }

func (p *TOTPProvider) digits() int {
	if p.Digits == 0 {
		return 6
	}
	return p.Digits
}

func (p *TOTPProvider) period() time.Duration {
	if p.Period == 0 {
		return 30 * time.Second
	}
	return p.Period
}

// Code computes an RFC 6238 TOTP for a base32 seed at time t.
func Code(seedB32 string, t time.Time, digits int, period time.Duration) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.ReplaceAll(seedB32, " ", "")))
	if err != nil {
		return "", fmt.Errorf("totp: bad seed: %w", err)
	}
	counter := uint64(t.Unix() / int64(period.Seconds()))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
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
