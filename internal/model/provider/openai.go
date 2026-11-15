// Package provider holds concrete LLMProvider implementations.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/model"
)

// OpenAICompat talks to any OpenAI-compatible chat-completions endpoint
// (OpenAI, Ollama, vLLM, LM Studio, OpenRouter, Azure …).
type OpenAICompat struct {
	BaseURL   string
	APIKey    string
	Model     string
	HTTP      *http.Client
	caps      model.Capabilities
	PriceIn   float64 // USD per 1M input tokens
	PriceOut  float64 // USD per 1M output tokens
	UserAgent string
}

// OpenAIOptions configures NewOpenAICompat.
type OpenAIOptions struct {
	BaseURL  string // default https://api.openai.com/v1 or $OPENAI_BASE_URL
	APIKey   string // default $OPENAI_API_KEY (may be empty for local servers)
	Model    string // default $OPENAI_MODEL or gpt-4o-mini
	JSONMode bool   // whether the endpoint honours response_format json_object
	Vision   bool
	PriceIn  float64
	PriceOut float64
	Timeout  time.Duration
}

// NewOpenAICompat constructs the provider, reading defaults from env.
func NewOpenAICompat(o OpenAIOptions) *OpenAICompat {
	base := o.BaseURL
	if base == "" {
		base = os.Getenv("OPENAI_BASE_URL")
	}
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	key := o.APIKey
	if key == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	m := o.Model
	if m == "" {
		m = os.Getenv("OPENAI_MODEL")
	}
	if m == "" {
		m = "gpt-4o-mini"
	}
	to := o.Timeout
	if to == 0 {
		to = 90 * time.Second
	}
	return &OpenAICompat{
		BaseURL:  strings.TrimRight(base, "/"),
		APIKey:   key,
		Model:    m,
		HTTP:     &http.Client{Timeout: to},
		PriceIn:  o.PriceIn,
		PriceOut: o.PriceOut,
		caps: model.Capabilities{
			NativeToolCalling: true,
			JSONMode:          o.JSONMode,
			Vision:            o.Vision,
			Streaming:         true,
			MaxContextTokens:  128000,
			MaxOutputTokens:   8192,
		},
		UserAgent: "agentbrowser",
	}
}

// Name implements model.LLMProvider.
func (p *OpenAICompat) Name() string { return "openai-compat/" + p.Model }

// Capabilities implements model.LLMProvider.
func (p *OpenAICompat) Capabilities() model.Capabilities { return p.caps }

type chatReq struct {
	Model          string           `json:"model"`
	Messages       []chatMsg        `json:"messages"`
	Temperature    *float64         `json:"temperature,omitempty"`
	MaxTokens      int              `json:"max_tokens,omitempty"`
	ResponseFormat *responseFormatT `json:"response_format,omitempty"`
}

type responseFormatT struct {
	Type string `json:"type"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *OpenAICompat) call(ctx context.Context, msgs []model.Message, opts model.ChatOpts, jsonMode bool) (*model.Completion, error) {
	req := chatReq{Model: p.Model, MaxTokens: opts.MaxTokens}
	if opts.Temperature > 0 {
		t := opts.Temperature
		req.Temperature = &t
	}
	for _, m := range msgs {
		req.Messages = append(req.Messages, chatMsg{Role: string(m.Role), Content: m.Text})
	}
	if jsonMode && p.caps.JSONMode {
		req.ResponseFormat = &responseFormatT{Type: "json_object"}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("User-Agent", p.UserAgent)
	if p.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := p.HTTP.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("openai: read: %w", err)
	}
	var cr chatResp
	if err := json.Unmarshal(data, &cr); err != nil {
		return nil, fmt.Errorf("openai: decode (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(data))
		if cr.Error != nil {
			msg = cr.Error.Message
		}
		return nil, fmt.Errorf("openai: status %d: %s", resp.StatusCode, msg)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices")
	}
	u := model.Usage{InputTokens: cr.Usage.PromptTokens, OutputTokens: cr.Usage.CompletionTokens}
	u.CostUSD = float64(u.InputTokens)/1e6*p.PriceIn + float64(u.OutputTokens)/1e6*p.PriceOut
	return &model.Completion{Text: cr.Choices[0].Message.Content, Usage: u}, nil
}

// Chat implements model.LLMProvider.
func (p *OpenAICompat) Chat(ctx context.Context, msgs []model.Message, opts model.ChatOpts) (*model.Completion, error) {
	return p.call(ctx, msgs, opts, false)
}

// Structured implements model.LLMProvider with the degradation ladder:
// JSON mode (when supported) → extract/validate → one repair round.
func (p *OpenAICompat) Structured(ctx context.Context, msgs []model.Message, schema json.RawMessage, opts model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	withSchema := append([]model.Message(nil), msgs...)
	withSchema = append(withSchema, model.Message{Role: model.System, Text: "Respond ONLY with a single JSON object matching this JSON Schema, no prose:\n" + string(schema)})
	c, err := p.call(ctx, withSchema, opts, true)
	if err != nil {
		return nil, nil, err
	}
	usage := c.Usage
	raw, err := model.ExtractJSON(c.Text)
	if err == nil {
		return raw, &usage, nil
	}
	// Repair round.
	repair := append(withSchema, model.Message{Role: model.Assistant, Text: c.Text}, model.Message{Role: model.User, Text: "That was not valid JSON. Return only the JSON object."})
	c2, err2 := p.call(ctx, repair, opts, true)
	if err2 != nil {
		return nil, &usage, err2
	}
	usage.InputTokens += c2.Usage.InputTokens
	usage.OutputTokens += c2.Usage.OutputTokens
	usage.CostUSD += c2.Usage.CostUSD
	raw, err = model.ExtractJSON(c2.Text)
	if err != nil {
		return nil, &usage, err
	}
	return raw, &usage, nil
}
