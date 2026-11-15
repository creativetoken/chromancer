package server

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// prompter mediates pending challenges (verification / human handoff) between
// the authenticator (which asks) and the caller (which answers via
// ResolveChallenge). M2 wires the mechanism; M3/M4 drive it.
type prompter struct {
	mu      sync.Mutex
	waiters map[string]*waiter
	current func(*Pending) // set by the JobManager to surface the pending state
}

type waiter struct {
	pending Pending
	ans     chan Answer
}

func newPrompter() *prompter {
	return &prompter{waiters: map[string]*waiter{}}
}

// Ask registers a pending challenge, surfaces it, and blocks until it is
// resolved via resolve() or ctx/deadline elapses.
func (p *prompter) Ask(ctx context.Context, pend Pending, surface func(*Pending)) (Answer, error) {
	w := &waiter{pending: pend, ans: make(chan Answer, 1)}
	p.mu.Lock()
	p.waiters[pend.ChallengeID] = w
	p.mu.Unlock()
	if surface != nil {
		surface(&pend)
	}
	defer func() {
		p.mu.Lock()
		delete(p.waiters, pend.ChallengeID)
		p.mu.Unlock()
		if surface != nil {
			surface(nil)
		}
	}()
	var deadline <-chan time.Time
	if !pend.ExpiresAt.IsZero() {
		d := time.Until(pend.ExpiresAt)
		t := time.NewTimer(d)
		defer t.Stop()
		deadline = t.C
	}
	select {
	case a := <-w.ans:
		return a, nil
	case <-deadline:
		return Answer{}, context.DeadlineExceeded
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

// resolve delivers an answer to a waiting challenge.
func (p *prompter) resolve(challengeID string, ans Answer) error {
	p.mu.Lock()
	w, ok := p.waiters[challengeID]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending challenge %q", challengeID)
	}
	select {
	case w.ans <- ans:
		return nil
	default:
		return fmt.Errorf("challenge %q already answered", challengeID)
	}
}
