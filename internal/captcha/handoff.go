package captcha

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// BlockReason says why the agent handed off to a human.
type BlockReason string

// Block reasons.
const (
	ReasonCaptchaExhausted BlockReason = "captcha_exhausted"
	ReasonBlockPage        BlockReason = "block_page"
	ReasonChallengeLoop    BlockReason = "challenge_loop"
	ReasonUnknownFlow      BlockReason = "unknown_flow"
	ReasonStuck            BlockReason = "stuck"
	ReasonVerifyTimeout    BlockReason = "verify_timeout"
)

// ErrHandoffTimeout is returned when no operator answers in time.
var ErrHandoffTimeout = fmt.Errorf("human handoff timed out")

// HandoffRequest describes a pending human handoff.
type HandoffRequest struct {
	ChallengeID string
	Reason      BlockReason
	ViewURL     string // localhost-bound or one-time-token-protected remote view ("" = local headful)
	ExpiresAt   time.Time
}

// Asker surfaces a human handoff to the operator and blocks for a "done"
// signal. The server bridges this to the MCP needs_human/resolve_challenge
// flow; a CLI prompt bridges it to the terminal. Returns ErrHandoffTimeout on
// deadline.
type Asker interface {
	AskHuman(ctx context.Context, req HandoffRequest) error
}

// Notifier is an optional hook fired when a handoff starts (desktop/webhook).
type Notifier func(req HandoffRequest)

// HumanHandoff coordinates escalation to a human for any dead end. It owns the
// one-time token that protects a remote view; it does not itself render the
// view (headful Chrome or a DevTools endpoint the operator opens).
type HumanHandoff struct {
	Asker    Asker
	Notify   Notifier
	Headful  bool   // if true, view_url is empty (operator acts at the machine)
	ViewBase string // e.g. "http://127.0.0.1:9222" for a remote DevTools/screencast view

	mu     sync.Mutex
	tokens map[string]bool
}

// NewHumanHandoff builds a HumanHandoff.
func NewHumanHandoff(asker Asker) *HumanHandoff {
	return &HumanHandoff{Asker: asker, Headful: true, tokens: map[string]bool{}}
}

func (h *HumanHandoff) mintToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	t := hex.EncodeToString(b)
	h.mu.Lock()
	h.tokens[t] = true
	h.mu.Unlock()
	return t
}

// ValidToken reports whether a one-time view token is currently valid.
func (h *HumanHandoff) ValidToken(t string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tokens[t]
}

func (h *HumanHandoff) revoke(t string) {
	h.mu.Lock()
	delete(h.tokens, t)
	h.mu.Unlock()
}

// Escalate surfaces a handoff and blocks until the operator signals done or the
// deadline passes. The remote view URL (when not headful) carries a one-time
// token that is revoked on return.
func (h *HumanHandoff) Escalate(ctx context.Context, reason BlockReason, deadline time.Time) error {
	if h.Asker == nil {
		return fmt.Errorf("human handoff required (%s) but no operator channel is configured", reason)
	}
	cid := "handoff-" + hexStamp()
	req := HandoffRequest{ChallengeID: cid, Reason: reason, ExpiresAt: deadline}
	var token string
	if !h.Headful && h.ViewBase != "" {
		token = h.mintToken()
		req.ViewURL = fmt.Sprintf("%s/?token=%s&challenge=%s", h.ViewBase, token, cid)
		defer h.revoke(token)
	}
	if h.Notify != nil {
		h.Notify(req)
	}
	vctx := ctx
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		vctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	err := h.Asker.AskHuman(vctx, req)
	if err != nil {
		if vctx.Err() == context.DeadlineExceeded {
			return ErrHandoffTimeout
		}
		return err
	}
	return nil
}

func hexStamp() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
