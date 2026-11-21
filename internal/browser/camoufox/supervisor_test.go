package camoufox

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRunner is a controllable sidecar stand-in. exit unblocks Wait (simulating
// a crash or clean exit); Close records the teardown.
type fakeRunner struct {
	id     int
	exit   chan error
	closed atomic.Bool
}

func newFakeRunner(id int) *fakeRunner { return &fakeRunner{id: id, exit: make(chan error, 1)} }
func (f *fakeRunner) Endpoint() string { return "ws://fake" }
func (f *fakeRunner) Wait() error      { return <-f.exit }
func (f *fakeRunner) Close() {
	if f.closed.CompareAndSwap(false, true) {
		select {
		case f.exit <- nil: // unblock Wait on close
		default:
		}
	}
}

func TestSupervisorRestartsOnCrash(t *testing.T) {
	var (
		mu       sync.Mutex
		made     []*fakeRunner
		launches int32
	)
	sup := &Supervisor{
		MaxRestarts: 2,
		Launch: func(ctx context.Context) (runner, error) {
			n := int(atomic.AddInt32(&launches, 1))
			r := newFakeRunner(n)
			mu.Lock()
			made = append(made, r)
			mu.Unlock()
			return r, nil
		},
		OnStart: func(r runner) error { return nil },
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sup.Stop()

	// Crash the first runner → supervisor should relaunch.
	mu.Lock()
	first := made[0]
	mu.Unlock()
	first.exit <- context.DeadlineExceeded // simulate an unexpected exit

	waitFor(t, 2*time.Second, func() bool { return atomic.LoadInt32(&launches) == 2 })

	cur := sup.Current()
	if cur == nil || cur.(*fakeRunner).id != 2 {
		t.Fatalf("expected the 2nd runner to be current after a crash, got %+v", cur)
	}

	// Crash again → 3rd launch (restart 2/2).
	mu.Lock()
	second := made[1]
	mu.Unlock()
	second.exit <- context.DeadlineExceeded
	waitFor(t, 2*time.Second, func() bool { return atomic.LoadInt32(&launches) == 3 })

	// Third crash exceeds MaxRestarts=2 → no more relaunch; Current becomes nil.
	mu.Lock()
	third := made[2]
	mu.Unlock()
	third.exit <- context.DeadlineExceeded
	waitFor(t, 2*time.Second, func() bool { return sup.Current() == nil })
	if got := atomic.LoadInt32(&launches); got != 3 {
		t.Fatalf("restart budget exceeded but launches = %d (want 3)", got)
	}
}

func TestSupervisorIdleShutdownAndRelaunch(t *testing.T) {
	var launches int32
	sup := &Supervisor{
		IdleTimeout: 40 * time.Millisecond,
		MaxRestarts: 1,
		Launch: func(ctx context.Context) (runner, error) {
			atomic.AddInt32(&launches, 1)
			return newFakeRunner(int(atomic.LoadInt32(&launches))), nil
		},
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sup.Stop()

	// With no Touch, the sidecar should be idle-shut.
	waitFor(t, 2*time.Second, func() bool { return sup.Current() == nil })

	// Ensure relaunches on demand.
	r, err := sup.Ensure()
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if r == nil || sup.Current() == nil {
		t.Fatal("Ensure should have relaunched the sidecar")
	}
	if atomic.LoadInt32(&launches) != 2 {
		t.Fatalf("launches = %d, want 2 (initial + relaunch)", atomic.LoadInt32(&launches))
	}
}

func TestSupervisorTouchDefersIdle(t *testing.T) {
	var launches int32
	sup := &Supervisor{
		IdleTimeout: 60 * time.Millisecond,
		Launch: func(ctx context.Context) (runner, error) {
			atomic.AddInt32(&launches, 1)
			return newFakeRunner(1), nil
		},
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sup.Stop()

	// Touch repeatedly for longer than one idle period; it must stay alive.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		sup.Touch()
		time.Sleep(15 * time.Millisecond)
	}
	if sup.Current() == nil {
		t.Fatal("sidecar was idle-shut despite continuous Touch")
	}
	if got := atomic.LoadInt32(&launches); got != 1 {
		t.Fatalf("launches = %d, want 1 (no relaunch while active)", got)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
