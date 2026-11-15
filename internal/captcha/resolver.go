package captcha

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

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
	entries       []solverEntry
	injectors     map[Vendor]Injector
	budget        Budget
	maxSolves     int
	audit         audit.AuditLog
	nativeWait    time.Duration // how long to wait for a native click to fill the field
	stats         *Stats        // optional per-(target,vendor,solver) stats
	target        string        // label for stats keys
	adaptive      bool          // reorder eligible solvers by observed success rate
	ua            string        // browser UA, forwarded to NeedsProxy solvers
	proxy         string        // upstream proxy, forwarded to NeedsProxy solvers
	solverTimeout time.Duration // per-solver Solve timeout (0 = none)

	mu       sync.Mutex
	tried    map[string]int    // vendor+sitekey -> count of solvers tried
	solverOf map[string]Solver // solveID -> solver (for Outcome)
	solveVnd map[string]Vendor // solveID -> vendor (for stats)
	seq      int
}

// ResolverOption configures the resolver.
type ResolverOption func(*chainResolver)

// WithStats records per-(target,vendor,solver) outcomes to the given store.
func WithStats(s *Stats, target string) ResolverOption {
	return func(r *chainResolver) { r.stats = s; r.target = target }
}

// WithAdaptiveOrder reorders eligible solvers by observed success rate.
func WithAdaptiveOrder(on bool) ResolverOption {
	return func(r *chainResolver) { r.adaptive = on }
}

// WithNativeWait sets how long to wait for a native click to fill the field.
func WithNativeWait(d time.Duration) ResolverOption {
	return func(r *chainResolver) { r.nativeWait = d }
}

// WithClientHints forwards the browser UA and upstream proxy to solvers that
// declare NeedsProxy (external services bind tokens to IP/UA).
func WithClientHints(ua, proxy string) ResolverOption {
	return func(r *chainResolver) { r.ua = ua; r.proxy = proxy }
}

// WithSolverTimeout bounds each solver's Solve call.
func WithSolverTimeout(d time.Duration) ResolverOption {
	return func(r *chainResolver) { r.solverTimeout = d }
}

// NewResolver builds a chainResolver. Describe() is called once per solver here.
func NewResolver(solvers []Solver, injectors map[Vendor]Injector, budget Budget, maxSolves int, al audit.AuditLog, opts ...ResolverOption) Resolver {
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
	r := &chainResolver{
		entries: entries, injectors: injectors, budget: budget, maxSolves: maxSolves, audit: al,
		nativeWait: 6 * time.Second,
		tried:      map[string]int{}, solverOf: map[string]Solver{}, solveVnd: map[string]Vendor{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
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
		if r.adaptive && r.stats != nil {
			// Observed success rate first (ties fall through to cheaper/faster).
			ri := r.stats.Rate(r.target, string(c.Vendor), out[i].s.Name())
			rj := r.stats.Rate(r.target, string(c.Vendor), out[j].s.Name())
			if ri != rj {
				return ri > rj
			}
		}
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
	// Forward client hints to external solvers that bind tokens to IP/UA.
	if entry.caps.NeedsProxy && (r.ua != "" || r.proxy != "") {
		if c.Params == nil {
			c.Params = map[string]string{}
		}
		if r.ua != "" {
			c.Params[ParamUserAgent] = r.ua
		}
		if r.proxy != "" {
			c.Params[ParamProxy] = r.proxy
		}
	}
	sctx := ctx
	if r.solverTimeout > 0 {
		var cancel context.CancelFunc
		sctx, cancel = context.WithTimeout(ctx, r.solverTimeout)
		defer cancel()
	}
	t0 := time.Now()
	res, err := entry.s.Solve(sctx, c)
	latency := time.Since(t0).Milliseconds()
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
	r.stats.RecordAttempt(r.target, string(c.Vendor), entry.s.Name(), latency, entry.caps.EstCostUSD)
	r.mu.Lock()
	r.solverOf[res.SolveID] = entry.s
	r.solveVnd[res.SolveID] = c.Vendor
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
	// After a native-style click (UIActions on a widget with a response field),
	// give the browser time to run its own verification and fill the token. If
	// nothing fills the field, native could not complete it: decline so the
	// chain advances to the next solver instead of submitting an empty token.
	if res.Kind == ResultUIActions && c.fieldSel != "" && p != nil {
		if !waitFieldFilled(ctx, p, c.fieldSel, r.nativeWait) {
			return Result{}, ErrDeclined
		}
	}
	r.logf("captcha_solve", map[string]any{"solver": entry.s.Name(), "vendor": string(c.Vendor), "kind": int(res.Kind), "solve_id": res.SolveID})
	return res, nil
}

// waitFieldFilled polls a response-field selector until it holds a non-empty
// value or the timeout elapses; it reports whether the field filled.
func waitFieldFilled(ctx context.Context, p *browser.Page, sel string, timeout time.Duration) bool {
	const js = `(sel) => { for (const s of sel.split(',')) { const el = document.querySelector(s.trim()); if (el && el.value) return true; } return false; }`
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return false
		}
		if v, err := p.Eval(ctx, js, sel); err == nil && v.Bool() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Outcome records whether the solve was accepted; forwarded to the solver's
// Report and to the stats store (then persisted).
func (r *chainResolver) Outcome(ctx context.Context, solveID string, accepted bool) error {
	r.mu.Lock()
	s := r.solverOf[solveID]
	vnd := r.solveVnd[solveID]
	delete(r.solveVnd, solveID)
	r.mu.Unlock()
	r.logf("captcha_outcome", map[string]any{"solve_id": solveID, "accepted": accepted})
	if s != nil && r.stats != nil {
		r.stats.RecordOutcome(r.target, string(vnd), s.Name(), accepted)
		_ = r.stats.Save()
	}
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
