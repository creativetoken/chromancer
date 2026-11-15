package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/handback"
)

// fakeRunner lets us drive job lifecycle without a browser.
type fakeRunner struct {
	mu      sync.Mutex
	gate    chan struct{} // if non-nil, RunAuth blocks until closed or ctx done
	result  handback.Result
	err     error
	reason  string
	started int
	ctxErr  error // captured ctx.Err() at return
}

func (f *fakeRunner) RunAuth(ctx context.Context, account string, mode auth.Mode, hints auth.Hints) app.AuthResult {
	f.mu.Lock()
	f.started++
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			f.mu.Lock()
			f.ctxErr = ctx.Err()
			f.mu.Unlock()
			return app.AuthResult{Err: ctx.Err(), Reason: "cancelled"}
		}
	}
	return app.AuthResult{Result: f.result, Err: f.err, Reason: f.reason}
}

func (f *fakeRunner) ExportSession(ctx context.Context, account string) (handback.ArtefactRef, handback.SessionBundle, error) {
	return handback.ArtefactRef{URI: "agentbrowser://artefacts/" + account + "/bundle-x.json"}, handback.SessionBundle{Replayable: true, Origin: "http://app"}, nil
}

func TestJobLifecycleDone(t *testing.T) {
	r := &fakeRunner{result: handback.Result{Trace: handback.Trace{FlowVariant: "form"}}}
	m := NewJobManager(r)
	id, err := m.StartAuth(context.Background(), "alice", auth.Login, auth.Hints{})
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.Wait(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != StateDone || j.Result == nil || j.Result.Trace.FlowVariant != "form" {
		t.Fatalf("job: %+v", j)
	}
	if j.FinishedAt == nil {
		t.Fatal("finished_at not set")
	}
}

func TestJobFailureReason(t *testing.T) {
	r := &fakeRunner{err: errors.New("sso only"), reason: "sso_only"}
	m := NewJobManager(r)
	id, _ := m.StartAuth(context.Background(), "bob", auth.Login, auth.Hints{})
	j, _ := m.Wait(context.Background(), id)
	if j.State != StateFailed || j.Reason != "sso_only" || j.Error == "" {
		t.Fatalf("job: %+v", j)
	}
}

func TestPerAccountLock(t *testing.T) {
	r := &fakeRunner{gate: make(chan struct{})}
	m := NewJobManager(r)
	id1, err := m.StartAuth(context.Background(), "alice", auth.Login, auth.Hints{})
	if err != nil {
		t.Fatal(err)
	}
	// Second start on same account is rejected while the first runs.
	_, err = m.StartAuth(context.Background(), "alice", auth.Login, auth.Hints{})
	var busy ErrAccountBusy
	if !errors.As(err, &busy) || busy.JobID != id1 {
		t.Fatalf("expected ErrAccountBusy for %s, got %v", id1, err)
	}
	// Different account is allowed concurrently.
	if _, err := m.StartAuth(context.Background(), "carol", auth.Login, auth.Hints{}); err != nil {
		t.Fatalf("different account should start: %v", err)
	}
	// Release the first; account frees up.
	close(r.gate)
	if _, err := m.Wait(context.Background(), id1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartAuth(context.Background(), "alice", auth.Login, auth.Hints{}); err != nil {
		t.Fatalf("account should be free after job done: %v", err)
	}
}

func TestCancelJobReleasesAccount(t *testing.T) {
	r := &fakeRunner{gate: make(chan struct{})}
	m := NewJobManager(r)
	id, _ := m.StartAuth(context.Background(), "dave", auth.Login, auth.Hints{})
	if err := m.CancelJob(id); err != nil {
		t.Fatal(err)
	}
	j, _ := m.GetJob(id)
	if j.State != StateCancelled {
		t.Fatalf("want cancelled, got %s", j.State)
	}
	r.mu.Lock()
	ce := r.ctxErr
	r.mu.Unlock()
	if ce == nil {
		t.Fatal("runner did not observe context cancellation")
	}
	// Account freed.
	if _, err := m.StartAuth(context.Background(), "dave", auth.Login, auth.Hints{}); err != nil {
		t.Fatalf("account not freed after cancel: %v", err)
	}
}

func TestResolveChallengeNoPending(t *testing.T) {
	r := &fakeRunner{}
	m := NewJobManager(r)
	id, _ := m.StartAuth(context.Background(), "x", auth.Login, auth.Hints{})
	_, _ = m.Wait(context.Background(), id)
	if err := m.ResolveChallenge(id, "nope", Answer{Code: "1"}); err == nil {
		t.Fatal("expected error for unknown challenge")
	}
	if err := m.ResolveChallenge("missing", "c", Answer{}); err == nil {
		t.Fatal("expected error for unknown job")
	}
}

func TestPrompterAskResolve(t *testing.T) {
	p := newPrompter()
	got := make(chan Answer, 1)
	go func() {
		a, err := p.Ask(context.Background(), Pending{ChallengeID: "c1", Kind: "verification"}, nil)
		if err != nil {
			t.Errorf("ask: %v", err)
		}
		got <- a
	}()
	// Wait for registration then resolve.
	deadline := time.After(time.Second)
	for {
		if err := p.resolve("c1", Answer{Code: "123456"}); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("never registered")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case a := <-got:
		if a.Code != "123456" {
			t.Fatalf("answer: %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("ask did not return")
	}
}

func TestPrompterDeadline(t *testing.T) {
	p := newPrompter()
	_, err := p.Ask(context.Background(), Pending{ChallengeID: "c2", ExpiresAt: time.Now().Add(20 * time.Millisecond)}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
}
