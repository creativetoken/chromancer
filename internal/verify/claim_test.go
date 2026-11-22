package verify

import (
	"context"
	"testing"
)

// A code fetched but not entered must be re-readable: Unclaim releases the claim
// so a retry of the same verification step can read the same code again (the
// server often won't send another). Reproduces the live Bambu lockout where the
// code was claimed on fetch, entry hit a focus timeout, and every retry then saw
// the code as alreadyClaimed.
func TestClaimUnclaimAllowsRetry(t *testing.T) {
	s := NewClaimStore()
	id := "<msg-1@example.com>"
	if !s.Claim(id) {
		t.Fatal("first claim should win")
	}
	if s.Claim(id) {
		t.Fatal("second claim should lose while claimed")
	}
	if !s.Claimed(id) {
		t.Fatal("should report claimed")
	}
	// Entry failed — release the code.
	s.Unclaim(id)
	if s.Claimed(id) {
		t.Fatal("should be unclaimed after Unclaim")
	}
	if !s.Claim(id) {
		t.Fatal("retry must be able to claim the same code again")
	}
	s.Unclaim("") // no-op, must not panic
}

// Chain.Unclaim forwards to the child provider that owns the claim, and skips
// providers that do not implement Unclaimer.
func TestChainUnclaimForwards(t *testing.T) {
	claims := NewClaimStore()
	imap := &IMAPProvider{Claims: claims}
	twilio := &TwilioProvider{Claims: claims}
	var ch Chain = []Provider{nonClaimStub{}, twilio, imap} // stub must be skipped, not panic
	id := "SM123"
	claims.Claim(id)
	ch.Unclaim(id) // must reach the providers' ClaimStore
	if claims.Claimed(id) {
		t.Fatal("Chain.Unclaim should have released the claim via a child provider")
	}
}

// nonClaimStub is a provider with no claim support, to prove Chain.Unclaim skips
// providers that do not implement Unclaimer.
type nonClaimStub struct{}

func (nonClaimStub) Supports(Channel, string) bool { return false }
func (nonClaimStub) GetCode(context.Context, Request) (Result, error) {
	return Result{}, ErrUnsupported
}
func (nonClaimStub) Resend(context.Context, Request) error { return ErrUnsupported }
