package explore

import "context"

// hybridCrawl combines BFS breadth with LLM decisions for forms and ambiguous
// controls. Implemented in a later milestone; until then it runs BFS so the
// driver is selectable and safe.
//
// TODO(L4): BFS drives link/menu breadth; when a state has a fillable form or
// no useful links, hand that state to the LLM driver for one bounded sub-run,
// then resume BFS.
func (e *Explorer) hybridCrawl(ctx context.Context) (*Report, error) {
	rep, err := e.bfsCrawl(ctx)
	if rep != nil {
		rep.Driver = string(DriverHybrid)
	}
	return rep, err
}
