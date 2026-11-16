package explore

import (
	"context"

	"github.com/creativetoken/chromancer/internal/core"
	"github.com/creativetoken/chromancer/internal/perceive"
)

// hybridCrawl combines BFS breadth with LLM decisions. BFS drives the cheap
// breadth (links and navigation menus, dedup, re-login); when it reaches a state
// with a fillable form, that form is handed to the LLM, which fills it with
// context-aware data and submits it. This reuses the shared BFS loop and
// Executor, so the deny-list, audit and re-login apply unchanged.
//
// Frontier-choice delegation (asking the LLM which queued link to visit next
// when several are pending) is a future refinement; today BFS orders the
// frontier and the LLM owns forms.
func (e *Explorer) hybridCrawl(ctx context.Context) (*Report, error) {
	if e.Model == nil {
		return nil, errNoModel("hybrid")
	}
	e.hybrid = true
	rep, err := e.bfsCrawl(ctx)
	if rep != nil {
		rep.Driver = string(DriverHybrid)
	}
	return rep, err
}

func errNoModel(driver string) error {
	return &noModelError{driver}
}

type noModelError struct{ driver string }

func (e *noModelError) Error() string { return "explore: " + e.driver + " driver needs a Model" }

// stateHasForm reports whether the state has a fillable input plus a submit
// control (worth handing to the LLM).
func stateHasForm(st perceive.State) bool {
	hasInput, hasSubmit := false, false
	for _, n := range st.Nodes {
		if !n.Visible || n.Disabled {
			continue
		}
		switch {
		case n.Tag == "textarea",
			n.Tag == "input" && n.Type != "submit" && n.Type != "button" && n.Type != "hidden" && n.Type != "checkbox" && n.Type != "radio",
			n.Tag == "select":
			hasInput = true
		case (n.Tag == "button" && (n.Type == "submit" || n.Type == "")) || (n.Tag == "input" && n.Type == "submit"):
			hasSubmit = true
		}
	}
	return hasInput && hasSubmit
}

// llmFillForm runs a short, focused LLM sub-loop to fill and submit the form on
// the current state. It returns true if a form was submitted. It shares the
// Report (forms/denied/tokens) and Executor with the rest of the crawl.
func (e *Explorer) llmFillForm(ctx context.Context, st perceive.State, rep *Report) bool {
	ex := e.executor()
	mem := &memory{}
	mem.note("Focus: fill in the form on this page with sensible values and submit it. Use {{account.*}} placeholders for the signed-in account's own credentials. When the form is submitted or there is nothing to fill, call done.")
	formsBefore := rep.FormsSubmitted
	maxSub := 5
	cur := st
	for i := 0; i < maxSub; i++ {
		if e.Budget != nil {
			if err := e.Budget.Check(core.ResSteps, 1); err != nil {
				return rep.FormsSubmitted > formsBefore
			}
			e.Budget.Spend(core.ResSteps, 1)
		}
		_, hash, _ := e.perceive(ctx)
		obs := e.observe(cur, hash, mem, maxSub-i, false)
		act, usage := e.decide(ctx, obs)
		if usage != nil {
			rep.InTokens += usage.InputTokens
			rep.OutTokens += usage.OutputTokens
			rep.CostUSD += usage.CostUSD
			rep.Steps++
		}
		ts := TraceStep{N: rep.Steps, Action: act.Action, Why: act.Why, URL: cur.URL, StateID: shortHash(hash)}
		done, _ := e.applyAction(ctx, ex, cur, act, &ts, rep, basePrefix(cur.URL), mem)
		rep.Trace = append(rep.Trace, ts)
		if done || rep.FormsSubmitted > formsBefore {
			break
		}
		_ = e.Page.WaitStable(ctx, e.PerAction)
		cur, _, _ = e.perceive(ctx)
	}
	return rep.FormsSubmitted > formsBefore
}
