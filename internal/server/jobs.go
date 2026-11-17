// Package server exposes the agent as a job-based control surface (CLI + MCP):
// start_auth, get_job, resolve_challenge, cancel_job, export_session. A
// per-account lock ensures one operation drives an account's context at a time.
package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/creativetoken/chromancer/internal/app"
	"github.com/creativetoken/chromancer/internal/auth"
	"github.com/creativetoken/chromancer/internal/handback"
)

// JobState is the polled status of an auth job.
type JobState string

// Job states.
const (
	StateRunning           JobState = "running"
	StateNeedsVerification JobState = "needs_verification"
	StateNeedsHuman        JobState = "needs_human"
	StateDone              JobState = "done"
	StateFailed            JobState = "failed"
	StateCancelled         JobState = "cancelled"
)

// Pending is a challenge the caller must answer (verification or human handoff).
type Pending struct {
	ChallengeID string    `json:"challenge_id"`
	Kind        string    `json:"kind"`              // verification | human
	Channel     string    `json:"channel,omitempty"` // email|sms|totp|push
	Hint        string    `json:"hint,omitempty"`
	ViewURL     string    `json:"view_url,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

// Recovering reports that the LLM agent is driving the live page to finish a
// flow the deterministic automation could not. Surfaced on get_job while the
// agent is in control.
type Recovering struct {
	Step   int    `json:"step"`
	Reason string `json:"reason"`
}

// Answer is the response to a Pending challenge.
type Answer struct {
	Code     string `json:"code,omitempty"`
	Link     string `json:"link,omitempty"`
	Approved bool   `json:"approved,omitempty"`
	Done     bool   `json:"done,omitempty"`
}

// Job is a running or finished auth/crawl job (JSON-serialisable for get_job).
type Job struct {
	ID         string           `json:"id"`
	Account    string           `json:"account"`
	Mode       string           `json:"mode"`
	Kind       string           `json:"kind"` // auth | crawl
	State      JobState         `json:"state"`
	Pending    *Pending         `json:"pending,omitempty"`
	Recovering *Recovering      `json:"recovering,omitempty"`
	Result     *handback.Result `json:"result,omitempty"`
	Crawl      *app.CrawlResult `json:"crawl,omitempty"`
	Reason     string           `json:"reason,omitempty"`
	Error      string           `json:"error,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`

	cancel context.CancelFunc
	doneCh chan struct{}
	prompt *prompter
}

// Runner performs the actual authentication and session export. *app.App
// satisfies it; tests use a fake. asker (may be nil) bridges verification to
// the MCP pending/answer surface for this job.
type Runner interface {
	RunAuth(ctx context.Context, account string, mode auth.Mode, hints auth.Hints, asker app.Asker) app.AuthResult
	RunCrawl(ctx context.Context, account string, opts app.CrawlOptions, asker app.Asker) app.CrawlResult
	ExportSession(ctx context.Context, account string) (handback.ArtefactRef, handback.SessionBundle, error)
}

// JobManager owns job lifecycle and the per-account lock.
type JobManager struct {
	runner Runner
	mu     sync.Mutex
	jobs   map[string]*Job
	active map[string]string // account -> active job id (non-terminal)
	seq    int
}

// NewJobManager creates a JobManager over runner.
func NewJobManager(runner Runner) *JobManager {
	return &JobManager{runner: runner, jobs: map[string]*Job{}, active: map[string]string{}}
}

// ErrAccountBusy is returned when an account already has a non-terminal job.
type ErrAccountBusy struct{ Account, JobID string }

func (e ErrAccountBusy) Error() string {
	return fmt.Sprintf("account %q is busy with job %s", e.Account, e.JobID)
}

func terminal(s JobState) bool {
	return s == StateDone || s == StateFailed || s == StateCancelled
}

// StartAuth launches an auth job and returns its id. It fails with
// ErrAccountBusy if the account already has a non-terminal job (per-account lock).
func (m *JobManager) StartAuth(parent context.Context, account string, mode auth.Mode, hints auth.Hints) (string, error) {
	m.mu.Lock()
	if id, ok := m.active[account]; ok {
		busy := ErrAccountBusy{Account: account, JobID: id}
		m.mu.Unlock()
		return "", busy
	}
	m.seq++
	id := fmt.Sprintf("job-%d-%d", time.Now().Unix(), m.seq)
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	j := &Job{
		ID: id, Account: account, Mode: mode.String(), State: StateRunning,
		StartedAt: time.Now().UTC(), cancel: cancel, doneCh: make(chan struct{}),
		prompt: newPrompter(),
	}
	j.Kind = "auth"
	m.jobs[id] = j
	m.active[account] = id
	m.mu.Unlock()

	go m.run(ctx, j, mode, hints)
	return id, nil
}

// StartCrawl launches an authenticated-crawl job (Phase 2) for an account.
func (m *JobManager) StartCrawl(parent context.Context, account string, opts app.CrawlOptions) (string, error) {
	m.mu.Lock()
	if id, ok := m.active[account]; ok {
		busy := ErrAccountBusy{Account: account, JobID: id}
		m.mu.Unlock()
		return "", busy
	}
	m.seq++
	id := fmt.Sprintf("crawl-%d-%d", time.Now().Unix(), m.seq)
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	j := &Job{ID: id, Account: account, Kind: "crawl", State: StateRunning, StartedAt: time.Now().UTC(),
		cancel: cancel, doneCh: make(chan struct{}), prompt: newPrompter()}
	m.jobs[id] = j
	m.active[account] = id
	m.mu.Unlock()

	go m.runCrawl(ctx, j, opts)
	return id, nil
}

func (m *JobManager) runCrawl(ctx context.Context, j *Job, opts app.CrawlOptions) {
	defer close(j.doneCh)
	res := m.runner.RunCrawl(ctx, j.Account, opts, &jobAsker{m: m, j: j})
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	j.FinishedAt = &now
	j.Pending = nil
	switch {
	case res.Err == nil:
		j.State = StateDone
		j.Crawl = &res
	case ctx.Err() != nil:
		j.State = StateCancelled
		j.Reason = "cancelled"
	default:
		j.State = StateFailed
		j.Reason = res.Reason
		if j.Reason == "" {
			j.Reason = "flow_failed"
		}
		j.Error = res.Err.Error()
	}
	delete(m.active, j.Account)
}

func (m *JobManager) run(ctx context.Context, j *Job, mode auth.Mode, hints auth.Hints) {
	defer close(j.doneCh)
	res := m.runner.RunAuth(ctx, j.Account, mode, hints, &jobAsker{m: m, j: j})
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	j.FinishedAt = &now
	j.Pending = nil
	switch {
	case res.Err == nil:
		j.State = StateDone
		r := res.Result
		j.Result = &r
	case ctx.Err() != nil:
		j.State = StateCancelled
		j.Reason = "cancelled"
	default:
		j.State = StateFailed
		j.Reason = res.Reason
		if j.Reason == "" {
			j.Reason = "flow_failed"
		}
		j.Error = res.Err.Error()
	}
	delete(m.active, j.Account)
}

// GetJob returns a snapshot of a job.
func (m *JobManager) GetJob(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("job %q not found", id)
	}
	return snapshot(j), nil
}

func snapshot(j *Job) Job {
	out := *j
	out.cancel = nil
	out.doneCh = nil
	out.prompt = nil
	return out
}

// ResolveChallenge answers a pending verification or human-handoff challenge.
func (m *JobManager) ResolveChallenge(id, challengeID string, ans Answer) error {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %q not found", id)
	}
	return j.prompt.resolve(challengeID, ans)
}

// CancelJob cancels a running job and waits briefly for cleanup.
func (m *JobManager) CancelJob(id string) error {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %q not found", id)
	}
	m.mu.Lock()
	done := terminal(j.State)
	m.mu.Unlock()
	if done {
		return nil
	}
	j.cancel()
	select {
	case <-j.doneCh:
	case <-time.After(30 * time.Second):
	}
	return nil
}

// ExportSession returns the latest bundle reference + bundle for an account.
func (m *JobManager) ExportSession(ctx context.Context, account string) (handback.ArtefactRef, handback.SessionBundle, error) {
	return m.runner.ExportSession(ctx, account)
}

// Wait blocks until the job reaches a terminal state (test helper).
func (m *JobManager) Wait(ctx context.Context, id string) (Job, error) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return Job{}, fmt.Errorf("job %q not found", id)
	}
	select {
	case <-j.doneCh:
		return m.GetJob(id)
	case <-ctx.Done():
		return Job{}, ctx.Err()
	}
}
