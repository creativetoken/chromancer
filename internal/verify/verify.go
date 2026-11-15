// Package verify obtains verification codes/links/approvals the agent needs
// during signup/login: THE AGENT ASKS, THE OPERATOR'S TOOL ANSWERS.
// Implementations: built-in TOTP, lab inbox (tests), CLI prompt, MCP
// pending/answer bridge, and webhook.
package verify

import (
	"context"
	"errors"
	"time"
)

// Channel is the verification method the page asks for.
type Channel string

// Channels.
const (
	Email Channel = "email"
	SMS   Channel = "sms"
	TOTP  Channel = "totp"
	Push  Channel = "push"
)

// Request is what the agent hands a provider.
type Request struct {
	Channel  Channel
	Account  string
	Target   string // app/site the login is for (used to pick a per-target TOTP seed)
	Hint     string // "code sent to ***1234"
	WantLink bool   // the page expects a magic link rather than a code
	Since    time.Time
	Deadline time.Time
}

// ResultKind discriminates the verification result.
type ResultKind int

// Result kinds.
const (
	ResCode     ResultKind = iota // one-time code to type
	ResLink                       // magic link to open
	ResApproved                   // push approval granted out of band
)

// Result carries whatever the page needs next.
type Result struct {
	Kind ResultKind
	Code string
	Link string
}

// Sentinel errors.
var (
	ErrTimeout     = errors.New("verification timed out")
	ErrDeclined    = errors.New("verification declined")
	ErrUnsupported = errors.New("verification channel unsupported")
)

// Provider is the pluggable way the agent obtains codes/links.
type Provider interface {
	// Supports reports whether this provider can service the channel for account.
	Supports(ch Channel, account string) bool
	// GetCode returns the code/link/approval, bounded by ctx/Deadline.
	GetCode(ctx context.Context, req Request) (Result, error)
	// Resend re-triggers delivery where supported; the caller then GetCodes again
	// with an advanced Since.
	Resend(ctx context.Context, req Request) error
}

// Chain tries providers in order, using the first that Supports the channel.
type Chain []Provider

// Supports implements Provider.
func (c Chain) Supports(ch Channel, account string) bool {
	for _, p := range c {
		if p.Supports(ch, account) {
			return true
		}
	}
	return false
}

// GetCode implements Provider.
func (c Chain) GetCode(ctx context.Context, req Request) (Result, error) {
	for _, p := range c {
		if p.Supports(req.Channel, req.Account) {
			return p.GetCode(ctx, req)
		}
	}
	return Result{}, ErrUnsupported
}

// Resend implements Provider.
func (c Chain) Resend(ctx context.Context, req Request) error {
	for _, p := range c {
		if p.Supports(req.Channel, req.Account) {
			return p.Resend(ctx, req)
		}
	}
	return ErrUnsupported
}
