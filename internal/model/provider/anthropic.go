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

// Anthropic talks to the Anthropic Messages API.
type Anthropic struct {
	BaseURL  string
	APIKey   string
	Model    string
	Version  string
	HTTP     *http.Client
	caps     model.Capabilities
	PriceIn  float64 // USD per 1M input tokens
	PriceOut float64
}

// AnthropicOptions configures NewAnthropic.
type AnthropicOptions struct {
	BaseURL  string // default https://api.anthropic.com or $ANTHROPIC_BASE_URL
	APIKey   string // default $ANTHROPIC_API_KEY
	Model    string // default $ANTHROPIC_MODEL or claude-sonnet-5-5
	Vision   bool
	PriceIn  float64
	PriceOut float64
	Timeout  time.Duration
}

// NewAnthropic constructs the provider, reading defaults from env.
func NewAnthropic(o AnthropicOptions) *Anthropic {
	base := firstNonEmpty(o.BaseURL, os.Getenv("ANTHROPIC_BASE_URL"), "https://api.anthropic.com")
	key := firstNonEmpty(o.APIKey, os.Getenv("ANTHROPIC_API_KEY"))
	m := firstNonEmpty(o.Model, os.Getenv("ANTHROPIC_MODEL"), "claude-sonnet-5-5")
	to := o.Timeout
	if to == 0 {
		to = 90 * time.Second
	}
	return &Anthropic{
		BaseURL: strings.TrimRight(base, "/"), APIKey: key, Model: m, Version: "2023-06-01",
		HTTP: &http.Client{Timeout: to}, PriceIn: o.PriceIn, PriceOut: o.PriceOut,
		caps: model.Capabilities{NativeToolCalling: true, Vision: o.Vision, Streaming: true, MaxContextTokens: 200000, MaxOutputTokens: 8192},
	}
}

// Name implements model.LLMProvider.
func (p *Anthropic) Name() string { return "anthropic/" + p.Model }

// Capabilities implements model.LLMProvider.
func (p *Anthropic) Capabilities() model.Capabilities { return p.caps }

type anthReq struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []anthMsg `json:"messages"`
	Temp      *float64  `json:"temperature,omitempty"`
}

type anthMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *Anthropic) call(ctx context.Context, msgs []model.Message, opts model.ChatOpts) (*model.Completion, error) {
	mt := opts.MaxTokens
	if mt == 0 {
		mt = 2048
	}
	req := anthReq{Model: p.Model, MaxTokens: mt}
	if opts.Temperature > 0 {
		t := opts.Temperature
		req.Temp = &t
	}
	for _, m := range msgs {
		if m.Role == model.System {
			if req.System != "" {
				req.System += "\n"
			}
			req.System += m.Text
			continue
		}
		role := "user"
		if m.Role == model.Assistant {
			role = "assistant"
		}
		req.Messages = append(req.Messages, anthMsg{Role: role, Content: m.Text})
	}
	body, _ := json.Marshal(req)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("x-api-key", p.APIKey)
	hreq.Header.Set("anthropic-version", p.Version)
	hreq.Header.Set("User-Agent", "agentbrowser")
	resp, err := p.HTTP.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: http: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var ar anthResp
	if err := json.Unmarshal(data, &ar); err != nil {
		return nil, fmt.Errorf("anthropic: decode (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(data))
		if ar.Error != nil {
			msg = ar.Error.Message
		}
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, msg)
	}
	var text strings.Builder
	for _, c := range ar.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	u := model.Usage{InputTokens: ar.Usage.InputTokens, OutputTokens: ar.Usage.OutputTokens}
	u.CostUSD = float64(u.InputTokens)/1e6*p.PriceIn + float64(u.OutputTokens)/1e6*p.PriceOut
	return &model.Completion{Text: text.String(), Usage: u}, nil
}

// Chat implements model.LLMProvider.
func (p *Anthropic) Chat(ctx context.Context, msgs []model.Message, opts model.ChatOpts) (*model.Completion, error) {
	return p.call(ctx, msgs, opts)
}

// Structured implements model.LLMProvider with the extract/repair ladder.
func (p *Anthropic) Structured(ctx context.Context, msgs []model.Message, schema json.RawMessage, opts model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	return structuredViaChat(ctx, p, msgs, schema, opts)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// structuredViaChat is the shared structured-output ladder for providers whose
// native path is plain chat: instruct JSON, extract, one repair round.
func structuredViaChat(ctx context.Context, p model.LLMProvider, msgs []model.Message, schema json.RawMessage, opts model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	ms := append([]model.Message(nil), msgs...)
	ms = append(ms, model.Message{Role: model.System, Text: "Respond ONLY with a single JSON object matching this JSON Schema, no prose:\n" + string(schema)})
	c, err := p.Chat(ctx, ms, opts)
	if err != nil {
		return nil, nil, err
	}
	usage := c.Usage
	if raw, err := model.ExtractJSON(c.Text); err == nil {
		return raw, &usage, nil
	}
	repair := append(ms, model.Message{Role: model.Assistant, Text: c.Text}, model.Message{Role: model.User, Text: "That was not valid JSON. Return only the JSON object."})
	c2, err := p.Chat(ctx, repair, opts)
	if err != nil {
		return nil, &usage, err
	}
	usage.InputTokens += c2.Usage.InputTokens
	usage.OutputTokens += c2.Usage.OutputTokens
	usage.CostUSD += c2.Usage.CostUSD
	raw, err := model.ExtractJSON(c2.Text)
	if err != nil {
		return nil, &usage, err
	}
	return raw, &usage, nil
}
