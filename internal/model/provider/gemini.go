package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/creativetoken/chromancer/internal/model"
)

// Gemini talks to the Google Gemini generateContent API.
type Gemini struct {
	BaseURL  string
	APIKey   string
	Model    string
	HTTP     *http.Client
	caps     model.Capabilities
	PriceIn  float64
	PriceOut float64
}

// GeminiOptions configures NewGemini.
type GeminiOptions struct {
	BaseURL  string // default https://generativelanguage.googleapis.com or $GEMINI_BASE_URL
	APIKey   string // default $GEMINI_API_KEY
	Model    string // default $GEMINI_MODEL or gemini-1.5-flash
	Vision   bool
	PriceIn  float64
	PriceOut float64
	Timeout  time.Duration
}

// NewGemini constructs the provider, reading defaults from env.
func NewGemini(o GeminiOptions) *Gemini {
	base := firstNonEmpty(o.BaseURL, os.Getenv("GEMINI_BASE_URL"), "https://generativelanguage.googleapis.com")
	key := firstNonEmpty(o.APIKey, os.Getenv("GEMINI_API_KEY"))
	m := firstNonEmpty(o.Model, os.Getenv("GEMINI_MODEL"), "gemini-1.5-flash")
	to := o.Timeout
	if to == 0 {
		to = 90 * time.Second
	}
	return &Gemini{
		BaseURL: strings.TrimRight(base, "/"), APIKey: key, Model: m,
		HTTP: &http.Client{Timeout: to}, PriceIn: o.PriceIn, PriceOut: o.PriceOut,
		caps: model.Capabilities{NativeJSONSchema: true, JSONMode: true, Vision: o.Vision, Streaming: true, MaxContextTokens: 1000000, MaxOutputTokens: 8192},
	}
}

// Name implements model.LLMProvider.
func (p *Gemini) Name() string { return "gemini/" + p.Model }

// Capabilities implements model.LLMProvider.
func (p *Gemini) Capabilities() model.Capabilities { return p.caps }

type gemReq struct {
	Contents          []gemContent  `json:"contents"`
	SystemInstruction *gemContent   `json:"systemInstruction,omitempty"`
	GenerationConfig  *gemGenConfig `json:"generationConfig,omitempty"`
}

type gemContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []gemPart `json:"parts"`
}

type gemPart struct {
	Text string `json:"text"`
}

type gemGenConfig struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxOutputTokens  int      `json:"maxOutputTokens,omitempty"`
	ResponseMimeType string   `json:"responseMimeType,omitempty"`
}

type gemResp struct {
	Candidates []struct {
		Content struct {
			Parts []gemPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *Gemini) call(ctx context.Context, msgs []model.Message, opts model.ChatOpts, jsonMime bool) (*model.Completion, error) {
	req := gemReq{GenerationConfig: &gemGenConfig{MaxOutputTokens: opts.MaxTokens}}
	if opts.Temperature > 0 {
		t := opts.Temperature
		req.GenerationConfig.Temperature = &t
	}
	if jsonMime {
		req.GenerationConfig.ResponseMimeType = "application/json"
	}
	for _, m := range msgs {
		if m.Role == model.System {
			req.SystemInstruction = &gemContent{Parts: []gemPart{{Text: m.Text}}}
			continue
		}
		role := "user"
		if m.Role == model.Assistant {
			role = "model"
		}
		req.Contents = append(req.Contents, gemContent{Role: role, Parts: []gemPart{{Text: m.Text}}})
	}
	body, _ := json.Marshal(req)
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", p.BaseURL, url.PathEscape(p.Model), url.QueryEscape(p.APIKey))
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini: request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("User-Agent", "agentbrowser")
	resp, err := p.HTTP.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("gemini: http: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var gr gemResp
	if err := json.Unmarshal(data, &gr); err != nil {
		return nil, fmt.Errorf("gemini: decode (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(data))
		if gr.Error != nil {
			msg = gr.Error.Message
		}
		return nil, fmt.Errorf("gemini: status %d: %s", resp.StatusCode, msg)
	}
	var text strings.Builder
	if len(gr.Candidates) > 0 {
		for _, part := range gr.Candidates[0].Content.Parts {
			text.WriteString(part.Text)
		}
	}
	u := model.Usage{InputTokens: gr.UsageMetadata.PromptTokenCount, OutputTokens: gr.UsageMetadata.CandidatesTokenCount}
	u.CostUSD = float64(u.InputTokens)/1e6*p.PriceIn + float64(u.OutputTokens)/1e6*p.PriceOut
	return &model.Completion{Text: text.String(), Usage: u}, nil
}

// Chat implements model.LLMProvider.
func (p *Gemini) Chat(ctx context.Context, msgs []model.Message, opts model.ChatOpts) (*model.Completion, error) {
	return p.call(ctx, msgs, opts, false)
}

// Structured implements model.LLMProvider using Gemini's JSON mime type + ladder.
func (p *Gemini) Structured(ctx context.Context, msgs []model.Message, schema json.RawMessage, opts model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	ms := append([]model.Message(nil), msgs...)
	ms = append(ms, model.Message{Role: model.System, Text: "Respond ONLY with a single JSON object matching this JSON Schema:\n" + string(schema)})
	c, err := p.call(ctx, ms, opts, true)
	if err != nil {
		return nil, nil, err
	}
	usage := c.Usage
	raw, err := model.ExtractJSON(c.Text)
	if err != nil {
		return nil, &usage, err
	}
	return raw, &usage, nil
}
