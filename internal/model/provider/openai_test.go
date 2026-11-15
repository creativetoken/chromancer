package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"agentbrowser/internal/model"
)

func TestOpenAICompatStructuredWithRepair(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if rf, ok := req["response_format"].(map[string]any); !ok || rf["type"] != "json_object" {
			t.Errorf("expected json_object response_format, got %v", req["response_format"])
		}
		n := atomic.AddInt32(&calls, 1)
		content := `garbage first time`
		if n == 2 {
			content = `{"variant":"form"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer srv.Close()

	p := NewOpenAICompat(OpenAIOptions{BaseURL: srv.URL + "/v1", APIKey: "test-key", Model: "m", JSONMode: true, PriceIn: 1, PriceOut: 2})
	raw, usage, err := p.Structured(context.Background(), []model.Message{{Role: model.User, Text: "classify"}}, json.RawMessage(`{"type":"object"}`), model.ChatOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"variant":"form"}` {
		t.Fatalf("got %s", raw)
	}
	if calls != 2 {
		t.Fatalf("expected repair round, calls=%d", calls)
	}
	if usage.InputTokens != 20 || usage.OutputTokens != 10 || usage.CostUSD <= 0 {
		t.Fatalf("usage aggregation wrong: %+v", usage)
	}
	if !p.Capabilities().JSONMode || p.Name() != "openai-compat/m" {
		t.Fatalf("caps/name wrong")
	}
}

func TestOpenAICompatErrorSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()
	p := NewOpenAICompat(OpenAIOptions{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	_, err := p.Chat(context.Background(), []model.Message{{Role: model.User, Text: "hi"}}, model.ChatOpts{})
	if err == nil || !contains(err.Error(), "rate limited") {
		t.Fatalf("want rate limited error, got %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
