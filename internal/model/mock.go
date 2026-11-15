package model

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// MockProvider is a scripted LLMProvider for tests and offline runs. Respond
// is called with the concatenated prompt and returns the model's text; when
// nil, the mock answers with an empty JSON object.
type MockProvider struct {
	mu      sync.Mutex
	Respond func(prompt string) string
	Calls   []string
	caps    Capabilities
}

// NewMock creates a MockProvider with the given responder.
func NewMock(respond func(prompt string) string) *MockProvider {
	return &MockProvider{
		Respond: respond,
		caps:    Capabilities{JSONMode: true, MaxContextTokens: 32000, MaxOutputTokens: 4000},
	}
}

// Name implements LLMProvider.
func (m *MockProvider) Name() string { return "mock" }

// Capabilities implements LLMProvider.
func (m *MockProvider) Capabilities() Capabilities { return m.caps }

func (m *MockProvider) prompt(msgs []Message) string {
	var b []byte
	for _, msg := range msgs {
		b = append(b, []byte(fmt.Sprintf("[%s] %s\n", msg.Role, msg.Text))...)
	}
	return string(b)
}

// Chat implements LLMProvider.
func (m *MockProvider) Chat(ctx context.Context, msgs []Message, _ ChatOpts) (*Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := m.prompt(msgs)
	m.mu.Lock()
	m.Calls = append(m.Calls, p)
	resp := m.Respond
	m.mu.Unlock()
	text := "{}"
	if resp != nil {
		text = resp(p)
	}
	return &Completion{Text: text, Usage: Usage{InputTokens: len(p) / 4, OutputTokens: len(text) / 4}}, nil
}

// Structured implements LLMProvider.
func (m *MockProvider) Structured(ctx context.Context, msgs []Message, _ json.RawMessage, opts ChatOpts) (json.RawMessage, *Usage, error) {
	c, err := m.Chat(ctx, msgs, opts)
	if err != nil {
		return nil, nil, err
	}
	raw, err := ExtractJSON(c.Text)
	if err != nil {
		return nil, &c.Usage, err
	}
	return raw, &c.Usage, nil
}

// CallCount returns how many times the mock was invoked.
func (m *MockProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Calls)
}
