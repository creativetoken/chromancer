// Package core holds the typed action space, budget enforcement and the small
// contracts (Skill, Planner, Verifier) the agent loop is built from.
package core

import (
	"context"
	"fmt"
)

// ActionKind is the minimal typed action space. The LLM only ever emits these
// (never code); every browser interaction in the system is one of them.
type ActionKind string

// Action kinds.
const (
	Navigate ActionKind = "navigate"
	Click    ActionKind = "click"
	Type     ActionKind = "type"
	Select   ActionKind = "select"
	Scroll   ActionKind = "scroll"
	Wait     ActionKind = "wait"
	Upload   ActionKind = "upload"
	Back     ActionKind = "back"
	Extract  ActionKind = "extract"
	Assert   ActionKind = "assert"
)

// Handle identifies an interactive node in a perceived State. Zero means
// "no target" (e.g. Navigate / Wait / Back).
type Handle int

// Action is a typed, serialisable browser instruction. Target is a perception
// handle; Value carries text to type, an option to select, a URL to navigate
// to, or a file path to upload.
type Action struct {
	Kind   ActionKind        `json:"kind"`
	Target Handle            `json:"target,omitempty"`
	Value  string            `json:"value,omitempty"`
	Meta   map[string]string `json:"meta,omitempty"`
}

// String renders the action for logs (values are redacted by the audit layer).
func (a Action) String() string {
	if a.Target != 0 {
		return fmt.Sprintf("%s[#%d]", a.Kind, a.Target)
	}
	return string(a.Kind)
}

// ActionResult reports the outcome of executing one Action.
type ActionResult struct {
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	URL     string `json:"url,omitempty"`
	Elapsed int64  `json:"elapsed_ms"`
}

// Goal is a short natural-language description of what the current step is
// trying to achieve; it steers skills and the planner.
type Goal string

// Step is the unit of work the control loop executes: pick a skill or ask the
// planner, act, verify. It is kept deliberately small for v1.
type Step struct {
	Goal   Goal
	Action Action
}

// Verdict is the Verifier's judgement about the last action.
type Verdict struct {
	Achieved bool   `json:"achieved"`
	Stuck    bool   `json:"stuck"`
	LoggedIn bool   `json:"logged_in"`
	Error    string `json:"error,omitempty"`
}

// Planner is the LLM-backed fallback: given a goal and a serialised state it
// emits the next action. v1 only uses it for narrow judgment calls.
type Planner interface {
	Next(ctx context.Context, goal Goal, stateJSON string) (Action, error)
}
