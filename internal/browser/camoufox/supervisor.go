package camoufox

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// runner is the sidecar surface the Supervisor manages. *Sidecar implements it;
// tests substitute a fake so supervision is tested without a real browser.
type runner interface {
	Endpoint() string
	Wait() error // blocks until the process exits
	Close()
}

var _ runner = (*Sidecar)(nil)

// Supervisor keeps one sidecar alive for an account: it restarts the sidecar if
// it crashes mid-job (up to MaxRestarts), and shuts it down after IdleTimeout of
// inactivity, relaunching lazily on the next Ensure. OnStart runs after every
// (re)launch so the owner can (re)connect to the new endpoint.
type Supervisor struct {
	// Launch (re)starts a sidecar. Required.
	Launch func(ctx context.Context) (runner, error)
	// OnStart, if set, runs after each successful (re)launch with the new
	// runner, so the caller can reconnect (e.g. playwright Firefox.Connect).
	OnStart func(r runner) error
	// IdleTimeout shuts the sidecar down after this long with no Touch. 0 = never.
	IdleTimeout time.Duration
	// MaxRestarts caps crash restarts over the supervisor's life. 0 = no restarts.
	MaxRestarts int
	// Logger is optional.
	Logger func(format string, args ...any)
	// now/after are swappable for tests (default time.Now / time.After).
	now   func() time.Time
	after func(time.Duration) <-chan time.Time

	mu        sync.Mutex
	cur       runner
	lastTouch time.Time
	restarts  int
	stopped   bool
	idleWake  chan struct{} // nudges the monitor to recompute the idle deadline
	ctx       context.Context
	cancel    context.CancelFunc
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger(format, args...)
	}
}

func (s *Supervisor) clock() func() time.Time {
	if s.now != nil {
		return s.now
	}
	return time.Now
}

func (s *Supervisor) timer(d time.Duration) <-chan time.Time {
	if s.after != nil {
		return s.after(d)
	}
	return time.After(d)
}

// Start launches the sidecar, runs OnStart, and begins supervision. It returns
// the first launch error, if any.
func (s *Supervisor) Start(parent context.Context) error {
	s.mu.Lock()
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(parent)
		s.idleWake = make(chan struct{}, 1)
	}
	s.mu.Unlock()
	if err := s.launchLocked(); err != nil {
		return err
	}
	go s.monitor()
	return nil
}

// launchLocked (re)launches and wires OnStart. Caller must NOT hold s.mu.
func (s *Supervisor) launchLocked() error {
	r, err := s.Launch(s.ctx)
	if err != nil {
		return err
	}
	if s.OnStart != nil {
		if err := s.OnStart(r); err != nil {
			r.Close()
			return err
		}
	}
	s.mu.Lock()
	s.cur = r
	s.lastTouch = s.clock()()
	s.mu.Unlock()
	return nil
}

// Touch marks activity, deferring idle shutdown.
func (s *Supervisor) Touch() {
	s.mu.Lock()
	s.lastTouch = s.clock()()
	s.mu.Unlock()
	select {
	case s.idleWake <- struct{}{}:
	default:
	}
}

// Current returns the live sidecar runner (may be nil if idle-shut).
func (s *Supervisor) Current() runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Ensure returns a live sidecar, relaunching if it was idle-shut. It Touches.
func (s *Supervisor) Ensure() (runner, error) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil, fmt.Errorf("camoufox: supervisor stopped")
	}
	if s.cur != nil {
		r := s.cur
		s.lastTouch = s.clock()()
		s.mu.Unlock()
		return r, nil
	}
	s.mu.Unlock()
	if err := s.launchLocked(); err != nil {
		return nil, err
	}
	go s.monitor()
	return s.Current(), nil
}

// Stop tears the supervisor down for good.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cur := s.cur
	s.cur = nil
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	if cur != nil {
		cur.Close()
	}
}

// monitor watches the current sidecar for an unexpected exit (→ restart) and for
// idle timeout (→ shut down, relaunch lazily). It returns when there is no live
// sidecar to watch (idle-shut or stopped).
func (s *Supervisor) monitor() {
	for {
		s.mu.Lock()
		cur := s.cur
		stopped := s.stopped
		s.mu.Unlock()
		if stopped || cur == nil {
			return
		}

		exited := make(chan struct{})
		go func() { cur.Wait(); close(exited) }()

		// Idle tick: recompute remaining idle time whenever Touch nudges us.
		var idleCh <-chan time.Time
		if s.IdleTimeout > 0 {
			s.mu.Lock()
			remaining := s.IdleTimeout - s.clock()().Sub(s.lastTouch)
			s.mu.Unlock()
			if remaining < 0 {
				remaining = 0
			}
			idleCh = s.timer(remaining)
		}

		select {
		case <-exited:
			if s.handleCrash(cur) {
				continue // relaunched; watch the new one
			}
			return
		case <-s.idleWake:
			// Activity happened; loop to recompute the idle deadline.
			continue
		case <-idleCh:
			if s.handleIdle(cur) {
				return // idle-shut; relaunch lazily via Ensure
			}
			continue
		case <-s.ctx.Done():
			return
		}
	}
}

// handleCrash restarts after an unexpected exit, within MaxRestarts. Returns
// true if a new sidecar is now current.
func (s *Supervisor) handleCrash(crashed runner) bool {
	s.mu.Lock()
	if s.stopped || s.cur != crashed {
		s.mu.Unlock()
		return false // intentional shutdown/replacement, not a crash
	}
	if s.restarts >= s.MaxRestarts {
		s.cur = nil
		s.mu.Unlock()
		s.logf("camoufox: sidecar crashed and restart budget (%d) exhausted", s.MaxRestarts)
		return false
	}
	s.restarts++
	n := s.restarts
	s.cur = nil
	s.mu.Unlock()
	s.logf("camoufox: sidecar crashed; restart %d/%d", n, s.MaxRestarts)
	if err := s.launchLocked(); err != nil {
		s.logf("camoufox: sidecar restart failed: %v", err)
		return false
	}
	return true
}

// handleIdle shuts the idle sidecar down. Returns true (monitor exits; Ensure
// relaunches on demand).
func (s *Supervisor) handleIdle(idle runner) bool {
	s.mu.Lock()
	// Re-check: a Touch may have landed between the timer firing and here.
	if s.stopped || s.cur != idle {
		s.mu.Unlock()
		return s.cur == nil
	}
	if s.clock()().Sub(s.lastTouch) < s.IdleTimeout {
		s.mu.Unlock()
		return false // not actually idle; keep watching
	}
	s.cur = nil
	s.mu.Unlock()
	s.logf("camoufox: sidecar idle for %s; shutting down (relaunch on next use)", s.IdleTimeout)
	idle.Close()
	return true
}
