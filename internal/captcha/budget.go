package captcha

import "github.com/creativetoken/chromancer/internal/core"

// coreBudget adapts core.Budget to the captcha Budget (solves + solver cost).
type coreBudget struct{ b core.Budget }

// NewCoreBudget adapts a core.Budget for the resolver.
func NewCoreBudget(b core.Budget) Budget { return coreBudget{b: b} }

// Check implements Budget.
func (c coreBudget) Check(solves, costUSD float64) error {
	if c.b == nil {
		return nil
	}
	if err := c.b.Check(core.ResSolves, solves); err != nil {
		return err
	}
	return c.b.Check(core.ResSolverCost, costUSD)
}

// Spend implements Budget.
func (c coreBudget) Spend(solves, costUSD float64) {
	if c.b == nil {
		return
	}
	c.b.Spend(core.ResSolves, solves)
	c.b.Spend(core.ResSolverCost, costUSD)
}
