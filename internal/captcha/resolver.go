package captcha

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/creativetoken/chromancer/internal/audit"
	"github.com/creativetoken/chromancer/internal/browser"
)

// solverEntry pairs a solver with its cached capabilities (Describe once).
type solverEntry struct {
	s    Solver
	caps Capabilities
}

// chainResolver runs a per-vendor prefiltered chain of solvers within budget.
// For a given challenge instance it advances through the eligible solvers; on
// rejection the flow handler re-detects a fresh challenge before retrying.
type chainResolver struct {
	entries   []solverEntry
	injectors map[Vendor]Injector
	budget    Budget
	maxSolves int
	audit     audit.AuditLog

	mu       sync.Mutex
	tried    map[string]int    // vendor+sitekey -> count of solvers tried
	solverOf map[string]Solver // solveID -> solver (for Outcome)
	seq      int
}

// NewResolver builds a chainResolver. Describe() is called once per solver here.
func NewResolver(solvers []Solver, injectors map[Vendor]Injector, budget Budget, maxSolves int, al audit.AuditLog) Resolver {
	entries := make([]solverEntry, 0, len(solvers))
	for _, s := range solvers {
		entries = append(entries, solverEntry{s: s, caps: s.Describe()})
	}
	if injectors == nil {
		injectors = map[Vendor]Injector{}
	}
	if maxSolves <= 0 {
		maxSolves = 3
	}
	return &chainResolver{
		entries: entries, injectors: injectors, budget: budget, maxSolves: maxSolves, audit: al,
		tried: map[string]int{}, solverOf: map[string]Solver{},
	}
}

func key(c Challenge) string { return string(c.Vendor) + "|" + c.SiteKey }

// eligible returns the entries whose cached caps match the challenge, cheapest
// (lowest est cost, then latency) first.
func (r *chainResolver) eligible(c Challenge) []solverEntry {
	var out []solverEntry
	for _, e := range r.entries {
		if e.caps.Supports(c.Vendor, c.Kind) && (!e.caps.NeedsImage || len(c.Image) > 0) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].caps.EstCostUSD != out[j].caps.EstCostUSD {
			return out[i].caps.EstCostUSD < out[j].caps.EstCostUSD
		}
		return out[i].caps.EstLatencyMS < out[j].caps.EstLatencyMS
	})
	return out
}

// Exhausted reports whether every eligible solver for this challenge has been tried.
func (r *chainResolver) Exhausted(c Challenge) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	el := r.eligible(c)
	return len(el) == 0 || r.tried[key(c)] >= len(el) || r.tried[key(c)] >= r.maxSolves
}

// Resolve applies the next eligible solver's result to the page (via Injector)
// and returns it with its solveID. It does NOT submit.
func (r *chainResolver) Resolve(ctx context.Context, p *browser.Page, c Challenge) (Result, error) {
	r.mu.Lock()
	el := r.eligible(c)
	idx := r.tried[key(c)]
	if len(el) == 0 {
		r.mu.Unlock()
		return Result{}, ErrNoSolver
	}
	if idx >= len(el) || idx >= r.maxSolves {
		r.mu.Unlock()
		return Result{}, ErrExhausted
	}
	entry := el[idx]
	r.tried[key(c)] = idx + 1
	r.seq++
	r.mu.Unlock()

	if r.budget != nil {
		if err := r.budget.Check(1, entry.caps.EstCostUSD); err != nil {
			return Result{}, err
		}
	}
	res, err := entry.s.Solve(ctx, c)
	if err != nil {
		r.logf("captcha_solve", map[string]any{"solver": entry.s.Name(), "vendor": string(c.Vendor), "error": err.Error()})
		return Result{}, fmt.Errorf("captcha: solver %s: %w", entry.s.Name(), err)
	}
	if res.SolveID == "" {
		res.SolveID = fmt.Sprintf("solve-%d", r.seq)
	}
	if r.budget != nil {
		r.budget.Spend(1, entry.caps.EstCostUSD)
	}
	r.mu.Lock()
	r.solverOf[res.SolveID] = entry.s
	r.mu.Unlock()

	if inj, ok := r.injectors[c.Vendor]; ok {
		if err := inj.Apply(ctx, p, c, res); err != nil {
			return Result{}, err
		}
	} else if res.Kind != ResultHandoff {
		if err := DefaultInjector(c.Vendor).Apply(ctx, p, c, res); err != nil {
			return Result{}, err
		}
	}
	r.logf("captcha_solve", map[string]any{"solver": entry.s.Name(), "vendor": string(c.Vendor), "kind": int(res.Kind), "solve_id": res.SolveID})
	return res, nil
}

// Outcome records whether the solve was accepted; forwarded to the solver's Report.
func (r *chainResolver) Outcome(ctx context.Context, solveID string, accepted bool) error {
	r.mu.Lock()
	s := r.solverOf[solveID]
	r.mu.Unlock()
	r.logf("captcha_outcome", map[string]any{"solve_id": solveID, "accepted": accepted})
	if s == nil {
		return nil
	}
	return s.Report(ctx, solveID, accepted)
}

func (r *chainResolver) logf(kind string, payload map[string]any) {
	if r.audit != nil {
		_, _ = r.audit.Append(context.Background(), audit.Entry{Actor: "captcha", Kind: kind, Payload: payload})
	}
}
