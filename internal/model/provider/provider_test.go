package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/creativetoken/chromancer/internal/model"
)

func TestAnthropicRecordedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			w.WriteHeader(400)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["system"] == nil {
			// first call includes the schema instruction as system
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": `{"variant":"form"}`}},
			"usage":   map[string]any{"input_tokens": 12, "output_tokens": 4},
		})
	}))
	defer srv.Close()
	p := NewAnthropic(AnthropicOptions{BaseURL: srv.URL, APIKey: "k", Model: "claude-x", PriceIn: 3, PriceOut: 15})
	if p.Name() != "anthropic/claude-x" || !p.Capabilities().NativeToolCalling {
		t.Fatal("name/caps")
	}
	raw, usage, err := p.Structured(context.Background(), []model.Message{{Role: model.User, Text: "classify"}}, json.RawMessage(`{"type":"object"}`), model.ChatOpts{})
	if err != nil || string(raw) != `{"variant":"form"}` {
		t.Fatalf("structured: %s %v", raw, err)
	}
	if usage.InputTokens != 12 || usage.CostUSD <= 0 {
		t.Fatalf("usage: %+v", usage)
	}
}

func TestAnthropicErrorSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(529)
		_, _ = w.Write([]byte(`{"error":{"message":"overloaded"}}`))
	}))
	defer srv.Close()
	p := NewAnthropic(AnthropicOptions{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	_, err := p.Chat(context.Background(), []model.Message{{Role: model.User, Text: "hi"}}, model.ChatOpts{})
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("want overloaded, got %v", err)
	}
}

func TestGeminiRecordedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":generateContent") || r.URL.Query().Get("key") != "gk" {
			w.WriteHeader(400)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		gc, _ := req["generationConfig"].(map[string]any)
		if gc == nil || gc["responseMimeType"] != "application/json" {
			t.Errorf("expected JSON mime, got %v", gc)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates":    []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": `{"handle": 4}`}}}}},
			"usageMetadata": map[string]any{"promptTokenCount": 20, "candidatesTokenCount": 3},
		})
	}))
	defer srv.Close()
	p := NewGemini(GeminiOptions{BaseURL: srv.URL, APIKey: "gk", Model: "gemini-x", PriceIn: 0.5, PriceOut: 1.5})
	raw, usage, err := p.Structured(context.Background(), []model.Message{{Role: model.User, Text: "pick"}}, json.RawMessage(`{"type":"object"}`), model.ChatOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Handle int }
	if json.Unmarshal(raw, &out) != nil || out.Handle != 4 {
		t.Fatalf("raw %s", raw)
	}
	if usage.InputTokens != 20 || usage.CostUSD <= 0 || p.Name() != "gemini/gemini-x" {
		t.Fatalf("usage/name: %+v %s", usage, p.Name())
	}
}

// Live tests run only when the key env var is present.
func TestAnthropicLive(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	p := NewAnthropic(AnthropicOptions{})
	c, err := p.Chat(context.Background(), []model.Message{{Role: model.User, Text: "Reply with the single word OK."}}, model.ChatOpts{MaxTokens: 16})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(c.Text), "OK") {
		t.Fatalf("unexpected: %q", c.Text)
	}
}

func TestGeminiLive(t *testing.T) {
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY not set")
	}
	p := NewGemini(GeminiOptions{})
	c, err := p.Chat(context.Background(), []model.Message{{Role: model.User, Text: "Reply with the single word OK."}}, model.ChatOpts{MaxTokens: 16})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(c.Text), "OK") {
		t.Fatalf("unexpected: %q", c.Text)
	}
}
