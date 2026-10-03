package nonstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
)

func TestNonstreamHappyPath(t *testing.T) {
	// Mock OpenAI upstream
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth: %s", r.Header.Get("Authorization"))
		}
		resp := map[string]any{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"created": 1770000000,
			"model":   "gpt-4o",
			"choices": []any{
				map[string]any{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "Hello there!",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cli := client.New(client.TransportConfig{}, nil)
	exec := NewExecutor(Config{Client: cli})

	p := client.Provider{
		ID:       "test-openai",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  srv.URL,
		APIKey:   "test-key",
	}

	req := domain.Request{
		Model: "gpt-4o",
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "Hi"}}},
		},
	}

	rec := httptest.NewRecorder()
	res, err := exec.Execute(context.Background(), rec, Call{
		ClientProto:   domain.ProtocolOpenAI,
		Provider:      p,
		Request:       req,
		RawBody:       []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`),
		OverrideModel: "routed-model-name",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if res.Response.Model != "routed-model-name" {
		t.Errorf("model: got %q, want routed-model-name", res.Response.Model)
	}
	if len(res.Response.Content) == 0 || res.Response.Content[0].Text != "Hello there!" {
		t.Errorf("unexpected content: %+v", res.Response.Content)
	}
	if res.Usage.TotalTokens != 15 {
		t.Errorf("usage: got %d, want 15", res.Usage.TotalTokens)
	}
}

func TestNonstreamCrossProtocolAnthropicToOpenAI(t *testing.T) {
	// Provider speaks OpenAI, Client requested Anthropic
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":      "chatcmpl-cross",
			"object":  "chat.completion",
			"created": 1770000000,
			"model":   "gpt-4o",
			"choices": []any{
				map[string]any{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "Translated response",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     12,
				"completion_tokens": 4,
				"total_tokens":      16,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cli := client.New(client.TransportConfig{}, nil)
	exec := NewExecutor(Config{Client: cli})

	p := client.Provider{
		ID:       "prov-openai",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  srv.URL,
		APIKey:   "k",
	}

	req := domain.Request{
		Model: "claude-3-5-sonnet",
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "Hello"}}},
		},
	}

	rec := httptest.NewRecorder()
	res, err := exec.Execute(context.Background(), rec, Call{
		ClientProto: domain.ProtocolAnthropic,
		Provider:    p,
		Request:     req,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}

	// Verify Anthropic envelope returned to client
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"message"`) || !strings.Contains(body, `"role":"assistant"`) {
		t.Fatalf("expected Anthropic wire format, got: %s", body)
	}
	if !strings.Contains(body, "Translated response") {
		t.Fatalf("missing content in response: %s", body)
	}
	if res.Usage.InputTokens != 12 {
		t.Errorf("usage: got %d, want 12", res.Usage.InputTokens)
	}
}

func TestOversizedRequestBodyFailsFast413(t *testing.T) {
	// Test acceptance criterion: "Oversized bodies fail fast with correct 413 envelope"
	upstreamHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := client.New(client.TransportConfig{}, nil)

	// Limit request size to 100 bytes
	exec := NewExecutor(Config{
		MaxRequestBodyBytes: 100,
		Client:              cli,
	})

	p := client.Provider{
		ID:       "prov",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  srv.URL,
		APIKey:   "k",
	}

	largeBody := []byte(strings.Repeat("x", 200))

	protocols := []struct {
		proto domain.ProviderProtocol
		check func(t *testing.T, body string)
	}{
		{
			proto: domain.ProtocolOpenAI,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"type":"request_too_large"`) && !strings.Contains(body, `"code":"request_too_large"`) {
					t.Fatalf("OpenAI 413 format mismatch: %s", body)
				}
			},
		},
		{
			proto: domain.ProtocolAnthropic,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"type":"request_too_large"`) {
					t.Fatalf("Anthropic 413 format mismatch: %s", body)
				}
			},
		},
		{
			proto: domain.ProtocolGemini,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"code":413`) {
					t.Fatalf("Gemini 413 format mismatch: %s", body)
				}
			},
		},
	}

	for _, tc := range protocols {
		t.Run(string(tc.proto), func(t *testing.T) {
			rec := httptest.NewRecorder()
			_, err := exec.Execute(context.Background(), rec, Call{
				ClientProto: tc.proto,
				Provider:    p,
				Request:     domain.Request{Model: "m"},
				RawBody:     largeBody,
			})

			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status: got %d, want 413", rec.Code)
			}
			if upstreamHit {
				t.Fatal("fail fast violated: upstream was hit despite oversized body")
			}
			tc.check(t, rec.Body.String())
		})
	}
}

func TestOversizedResponseBodyFailsFast413(t *testing.T) {
	// Upstream returns 500 bytes when max response cap is 100 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + strings.Repeat("A", 400) + `"}}]}`))
	}))
	defer srv.Close()

	cli := client.New(client.TransportConfig{}, nil)

	exec := NewExecutor(Config{
		MaxResponseBodyBytes: 100,
		Client:               cli,
	})

	p := client.Provider{
		ID:       "prov",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  srv.URL,
		APIKey:   "k",
	}

	protocols := []struct {
		proto domain.ProviderProtocol
		check func(t *testing.T, body string)
	}{
		{
			proto: domain.ProtocolOpenAI,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"type":"request_too_large"`) {
					t.Fatalf("OpenAI 413 envelope mismatch: %s", body)
				}
			},
		},
		{
			proto: domain.ProtocolAnthropic,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"type":"request_too_large"`) {
					t.Fatalf("Anthropic 413 envelope mismatch: %s", body)
				}
			},
		},
		{
			proto: domain.ProtocolGemini,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"code":413`) {
					t.Fatalf("Gemini 413 envelope mismatch: %s", body)
				}
			},
		},
	}

	for _, tc := range protocols {
		t.Run(string(tc.proto), func(t *testing.T) {
			rec := httptest.NewRecorder()
			_, err := exec.Execute(context.Background(), rec, Call{
				ClientProto: tc.proto,
				Provider:    p,
				Request:     domain.Request{Model: "m"},
				RawBody:     []byte(`{}`),
			})

			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status: got %d, want 413", rec.Code)
			}
			tc.check(t, rec.Body.String())
		})
	}
}

func TestUpstreamErrorPropagation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded","type":"rate_limit_error","code":"rate_limit"}}`))
	}))
	defer srv.Close()

	cli := client.New(client.TransportConfig{}, nil)
	exec := NewExecutor(Config{Client: cli})

	p := client.Provider{
		ID:       "prov",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  srv.URL,
		APIKey:   "k",
	}

	rec := httptest.NewRecorder()
	_, err := exec.Execute(context.Background(), rec, Call{
		ClientProto: domain.ProtocolAnthropic,
		Provider:    p,
		Request:     domain.Request{Model: "m"},
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status: got %d, want 429", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Rate limit exceeded") || !strings.Contains(body, "rate_limit_error") {
		t.Fatalf("Anthropic error envelope mismatch: %s", body)
	}
}
