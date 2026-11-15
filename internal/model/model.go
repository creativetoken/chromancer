// Package model is the model-agnostic LLM abstraction: one LLMProvider
// interface with capability negotiation and a structured-output degradation
// ladder, plus a ModelRouter that picks a provider per task.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Capabilities describes what a concrete model/endpoint supports.
type Capabilities struct {
	NativeToolCalling  bool `json:"native_tool_calling"`
	NativeJSONSchema   bool `json:"native_json_schema"`
	JSONMode           bool `json:"json_mode"`
	GrammarConstrained bool `json:"grammar_constrained"`
	Vision             bool `json:"vision"`
	Streaming          bool `json:"streaming"`
	MaxContextTokens   int  `json:"max_context_tokens"`
	MaxOutputTokens    int  `json:"max_output_tokens"`
}

// Role is a chat role.
type Role string

// Chat roles.
const (
	System    Role = "system"
	User      Role = "user"
	Assistant Role = "assistant"
)

// Message is a provider-neutral chat message. Images are only sent to models
// whose Capabilities report Vision.
type Message struct {
	Role   Role     `json:"role"`
	Text   string   `json:"text"`
	Images [][]byte `json:"-"`
}

// Usage is per-call token/cost accounting.
type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Completion is a Chat result.
type Completion struct {
	Text  string `json:"text"`
	Usage Usage  `json:"usage"`
}

// ChatOpts tunes a call.
type ChatOpts struct {
	Temperature float64
	MaxTokens   int
}

// LLMProvider is the single abstraction that makes the agent model-agnostic.
type LLMProvider interface {
	Name() string
	Capabilities() Capabilities
	// Chat returns free text.
	Chat(ctx context.Context, msgs []Message, opts ChatOpts) (*Completion, error)
	// Structured returns one schema-valid JSON object using the strongest
	// mechanism the model supports, falling back to generate→validate→repair.
	Structured(ctx context.Context, msgs []Message, schema json.RawMessage, opts ChatOpts) (json.RawMessage, *Usage, error)
}

// TaskKind selects a provider via the router.
type TaskKind string

// Task kinds.
const (
	TaskClassify TaskKind = "classify"
	TaskPlan     TaskKind = "plan"
	TaskVerify   TaskKind = "verify"
	TaskVision   TaskKind = "vision"
)

// ModelRouter selects a provider per task from config.
type ModelRouter interface {
	For(task TaskKind) LLMProvider
}

// StaticRouter maps tasks to providers with a default fallback.
type StaticRouter struct {
	Default LLMProvider
	ByTask  map[TaskKind]LLMProvider
}

// For implements ModelRouter.
func (r *StaticRouter) For(task TaskKind) LLMProvider {
	if p, ok := r.ByTask[task]; ok && p != nil {
		return p
	}
	return r.Default
}

// ErrNoJSON is returned when no JSON object could be extracted from a response.
var ErrNoJSON = errors.New("model: no JSON object in response")

var jsonBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// ExtractJSON pulls the first JSON object out of free text (handles code
// fences and leading prose). It is the "validate/repair" rung of the ladder.
func ExtractJSON(s string) (json.RawMessage, error) {
	s = strings.TrimSpace(s)
	if m := jsonBlockRe.FindStringSubmatch(s); len(m) == 2 {
		s = m[1]
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil, ErrNoJSON
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				cand := s[start : i+1]
				if json.Valid([]byte(cand)) {
					return json.RawMessage(cand), nil
				}
				return nil, fmt.Errorf("%w: invalid JSON", ErrNoJSON)
			}
		}
	}
	return nil, ErrNoJSON
}
