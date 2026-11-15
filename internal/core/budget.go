package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ResourceKind names a budgeted resource.
type ResourceKind string

// Budgeted resources.
const (
	ResTime       ResourceKind = "time"
	ResRequests   ResourceKind = "requests"
	ResTokens     ResourceKind = "tokens"
	ResCostUSD    ResourceKind = "cost_usd"
	ResSolverCost ResourceKind = "solver_cost_usd"
	ResSolves     ResourceKind = "solves"
	ResSteps      ResourceKind = "steps"
)

// ErrBudgetExhausted is returned (wrapped) when any limit is exceeded.
var ErrBudgetExhausted = errors.New("budget exhausted")

// Limits holds the per-run caps. Zero means unlimited for that resource.
type Limits struct {
	Time       time.Duration
	Requests   float64
	Tokens     float64
	CostUSD    float64
	SolverCost float64
	Solves     float64
	Steps      float64
}

// BudgetState is a snapshot of spend vs limits.
type BudgetState struct {
	Spent  map[ResourceKind]float64 `json:"spent"`
	Limits map[ResourceKind]float64 `json:"limits"`
}

// Budget enforces per-run limits. Check is called before every LLM call, every
// solver attempt and at the top of every loop iteration; exceeding any limit
// cancels the run context (kill-switch).
type Budget interface {
	// Check reports whether spending amount more of kind would exceed its limit.
	Check(kind ResourceKind, amount float64) error
	// Spend records spend; it never blocks.
	Spend(kind ResourceKind, amount float64)
	// Snapshot returns current spend and limits.
	Snapshot() BudgetState
	// Context derives a context that is cancelled when the time limit elapses or
	// when any other limit is exceeded via Spend.
	Context(parent context.Context) (context.Context, context.CancelFunc)
}

type budget struct {
	mu     sync.Mutex
	spent  map[ResourceKind]float64
	limits map[ResourceKind]float64
	cancel context.CancelFunc
	start  time.Time
}

// NewBudget creates a Budget from Limits.
func NewBudget(l Limits) Budget {
	b := &budget{
		spent:  map[ResourceKind]float64{},
		limits: map[ResourceKind]float64{},
		start:  time.Now(),
	}
	set := func(k ResourceKind, v float64) {
		if v > 0 {
			b.limits[k] = v
		}
	}
	set(ResTime, l.Time.Seconds())
	set(ResRequests, l.Requests)
	set(ResTokens, l.Tokens)
	set(ResCostUSD, l.CostUSD)
	set(ResSolverCost, l.SolverCost)
	set(ResSolves, l.Solves)
	set(ResSteps, l.Steps)
	return b
}

func (b *budget) Check(kind ResourceKind, amount float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if kind == ResTime {
		if lim, ok := b.limits[ResTime]; ok && time.Since(b.start).Seconds() > lim {
			return fmt.Errorf("%w: time limit %.0fs", ErrBudgetExhausted, lim)
		}
		return nil
	}
	lim, ok := b.limits[kind]
	if !ok {
		return nil
	}
	if b.spent[kind]+amount > lim {
		return fmt.Errorf("%w: %s %.2f + %.2f > %.2f", ErrBudgetExhausted, kind, b.spent[kind], amount, lim)
	}
	return nil
}

func (b *budget) Spend(kind ResourceKind, amount float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent[kind] += amount
	if lim, ok := b.limits[kind]; ok && b.spent[kind] > lim && b.cancel != nil {
		b.cancel()
	}
}

func (b *budget) Snapshot() BudgetState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := BudgetState{Spent: map[ResourceKind]float64{}, Limits: map[ResourceKind]float64{}}
	for k, v := range b.spent {
		st.Spent[k] = v
	}
	for k, v := range b.limits {
		st.Limits[k] = v
	}
	st.Spent[ResTime] = time.Since(b.start).Seconds()
	return st
}

func (b *budget) Context(parent context.Context) (context.Context, context.CancelFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ctx context.Context
	var cancel context.CancelFunc
	if lim, ok := b.limits[ResTime]; ok {
		ctx, cancel = context.WithTimeout(parent, time.Duration(lim*float64(time.Second)))
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	b.cancel = cancel
	return ctx, cancel
}
