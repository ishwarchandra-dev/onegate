package proxy_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/mockprovider"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/fallback"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/ingest"
	"github.com/ishwarchandra-dev/onegate/internal/server"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

type e2eHarness struct {
	mockServer *mockprovider.Server
	proxyServer *httptest.Server
	client      *client.Client
	store       *storage.Store
	rawKey      string
	keyPrefix   string
}

func setupE2E(t *testing.T, resolver fallback.TargetResolver) *e2eHarness {
	t.Helper()

	mockSrv := mockprovider.NewServer()

	store, err := storage.Open(t.TempDir() + "/e2e.db")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate storage: %v", err)
	}

	pepper := []byte("e2e-pepper-secret")
	verifier := auth.NewVerifier(store, pepper)

	rawKey, prefix := auth.GenerateVirtualKey()
	if _, err := store.VirtualKeys().Create(domain.VirtualKey{
		Name:    "e2e-test-key",
		Prefix:  prefix,
		KeyHash: auth.HashVirtualKey(pepper, rawKey),
		Status:  domain.KeyActive,
	}); err != nil {
		t.Fatalf("create virtual key: %v", err)
	}

	cli := client.New(client.TransportConfig{}, nil)

	engine := fallback.NewEngine(fallback.Config{
		Resolver:             resolver,
		Client:               cli,
		MaxRequestBodyBytes:  1024 * 1024,
		MaxResponseBodyBytes: 1024 * 1024,
	})

	r := server.New(server.Options{Logger: nil})
	ingest.Register(r.Mux(), ingest.Deps{
		Auth:  verifier,
		Proxy: engine,
	})

	proxySrv := httptest.NewServer(r.Handler())

	return &e2eHarness{
		mockServer:  mockSrv,
		proxyServer: proxySrv,
		client:      cli,
		store:       store,
		rawKey:      rawKey,
		keyPrefix:   prefix,
	}
}

func (h *e2eHarness) Close() {
	h.proxyServer.Close()
	h.mockServer.Close()
	h.client.CloseIdleConnections()
	h.store.Close()
}

// ---------------------------------------------------------------------------
// 1. All Three Protocols: Happy Paths (Stream + Non-Stream)
// ---------------------------------------------------------------------------

func TestE2E_OpenAI_HappyPath(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var h *e2eHarness
	resolver := fallback.StaticResolver{
		Targets: []fallback.Target{
			{
				Provider: client.Provider{
					ID:       "mock-openai",
					Protocol: domain.ProtocolOpenAI,
					BaseURL:  "", // filled below
					APIKey:   "sk-mock",
				},
				Model: "gpt-4o",
			},
		},
	}

	h = setupE2E(t, nil)
	defer h.Close()

	// Update base URL to mock server
	resolver.Targets[0].Provider.BaseURL = h.mockServer.URL
	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()
	engine := fallback.NewEngine(fallback.Config{
		Resolver: resolver,
		Client:   cli,
	})
	r := server.New(server.Options{})
	ingest.Register(r.Mux(), ingest.Deps{
		Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
		Proxy: engine,
	})
	h.proxyServer.Config.Handler = r.Handler()

	// 1a. Non-streaming
	t.Run("NonStream", func(t *testing.T) {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.rawKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var res struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				TotalTokens int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(res.Choices) == 0 || res.Choices[0].Message.Content != "Hello from mock OpenAI" {
			t.Fatalf("unexpected content: %+v", res)
		}
		if res.Usage.TotalTokens != 15 {
			t.Fatalf("expected 15 tokens, got %d", res.Usage.TotalTokens)
		}
	})

	// 1b. Streaming
	t.Run("Stream", func(t *testing.T) {
		body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"Hello"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.rawKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("expected text/event-stream, got %s", resp.Header.Get("Content-Type"))
		}

		var (
			fullText string
			seenDone bool
			scanner  = bufio.NewScanner(resp.Body)
		)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimPrefix(line, "data: ")
				if payload == "[DONE]" {
					seenDone = true
					break
				}
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(payload), &chunk); err == nil {
					if len(chunk.Choices) > 0 {
						fullText += chunk.Choices[0].Delta.Content
					}
				}
			}
		}

		if !seenDone {
			t.Error("expected [DONE] frame at end of stream")
		}
		if fullText != "Hello from mock OpenAI" {
			t.Fatalf("expected text 'Hello from mock OpenAI', got %q", fullText)
		}
	})
}

func TestE2E_Anthropic_HappyPath(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	h := setupE2E(t, nil)
	defer h.Close()

	resolver := fallback.StaticResolver{
		Targets: []fallback.Target{
			{
				Provider: client.Provider{
					ID:       "mock-anthropic",
					Protocol: domain.ProtocolAnthropic,
					BaseURL:  h.mockServer.URL,
					APIKey:   "sk-ant-mock",
				},
				Model: "claude-3-5-sonnet-20241022",
			},
		},
	}
	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()
	engine := fallback.NewEngine(fallback.Config{
		Resolver: resolver,
		Client:   cli,
	})
	r := server.New(server.Options{})
	ingest.Register(r.Mux(), ingest.Deps{
		Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
		Proxy: engine,
	})
	h.proxyServer.Config.Handler = r.Handler()

	// 2a. Non-streaming
	t.Run("NonStream", func(t *testing.T) {
		body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":"Hello"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", h.rawKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var res struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens int64 `json:"input_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(res.Content) == 0 || res.Content[0].Text != "Hello from mock Anthropic" {
			t.Fatalf("unexpected content: %+v", res)
		}
	})

	// 2b. Streaming
	t.Run("Stream", func(t *testing.T) {
		body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Hello"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", h.rawKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var (
			fullText string
			seenStop bool
			scanner  = bufio.NewScanner(resp.Body)
		)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimPrefix(line, "data: ")
				var ev struct {
					Type  string `json:"type"`
					Delta struct {
						Text string `json:"text"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(payload), &ev); err == nil {
					if ev.Type == "content_block_delta" {
						fullText += ev.Delta.Text
					}
					if ev.Type == "message_stop" {
						seenStop = true
					}
				}
			}
		}

		if !seenStop {
			t.Error("expected message_stop event")
		}
		if fullText != "Hello from mock Anthropic" {
			t.Fatalf("expected 'Hello from mock Anthropic', got %q", fullText)
		}
	})
}

func TestE2E_Gemini_HappyPath(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	h := setupE2E(t, nil)
	defer h.Close()

	resolver := fallback.StaticResolver{
		Targets: []fallback.Target{
			{
				Provider: client.Provider{
					ID:       "mock-gemini",
					Protocol: domain.ProtocolGemini,
					BaseURL:  h.mockServer.URL,
					APIKey:   "mock-gemini-key",
				},
				Model: "gemini-1.5-pro",
			},
		},
	}
	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()
	engine := fallback.NewEngine(fallback.Config{
		Resolver: resolver,
		Client:   cli,
	})
	r := server.New(server.Options{})
	ingest.Register(r.Mux(), ingest.Deps{
		Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
		Proxy: engine,
	})
	h.proxyServer.Config.Handler = r.Handler()

	// 3a. Non-streaming
	t.Run("NonStream", func(t *testing.T) {
		body := `{"contents":[{"parts":[{"text":"Hello"}]}]}`
		url := fmt.Sprintf("%s/v1beta/models/gemini-1.5-pro:generateContent?key=%s", h.proxyServer.URL, h.rawKey)
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var res struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(res.Candidates) == 0 || len(res.Candidates[0].Content.Parts) == 0 ||
			res.Candidates[0].Content.Parts[0].Text != "Hello from mock Gemini" {
			t.Fatalf("unexpected content: %+v", res)
		}
	})

	// 3b. Streaming
	t.Run("Stream", func(t *testing.T) {
		body := `{"contents":[{"parts":[{"text":"Hello"}]}]}`
		url := fmt.Sprintf("%s/v1beta/models/gemini-1.5-pro:streamGenerateContent?alt=sse&key=%s", h.proxyServer.URL, h.rawKey)
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var (
			fullText string
			scanner  = bufio.NewScanner(resp.Body)
		)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimPrefix(line, "data: ")
				var chunk struct {
					Candidates []struct {
						Content struct {
							Parts []struct {
								Text string `json:"text"`
							} `json:"parts"`
						} `json:"content"`
					} `json:"candidates"`
				}
				if err := json.Unmarshal([]byte(payload), &chunk); err == nil {
					if len(chunk.Candidates) > 0 && len(chunk.Candidates[0].Content.Parts) > 0 {
						fullText += chunk.Candidates[0].Content.Parts[0].Text
					}
				}
			}
		}

		if fullText != "Hello from mock Gemini" {
			t.Fatalf("expected 'Hello from mock Gemini', got %q", fullText)
		}
	})
}

// ---------------------------------------------------------------------------
// 2. Cross-Protocol Translation (OpenAI -> Anthropic, OpenAI -> Gemini)
// ---------------------------------------------------------------------------

func TestE2E_CrossProtocol(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	h := setupE2E(t, nil)
	defer h.Close()

	// Client talks OpenAI wire format, Upstream target is Anthropic!
	resolver := fallback.StaticResolver{
		Targets: []fallback.Target{
			{
				Provider: client.Provider{
					ID:       "mock-anthropic-backend",
					Protocol: domain.ProtocolAnthropic,
					BaseURL:  h.mockServer.URL,
					APIKey:   "sk-ant-mock",
				},
				Model: "claude-3-5-sonnet",
			},
		},
	}
	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()
	engine := fallback.NewEngine(fallback.Config{
		Resolver: resolver,
		Client:   cli,
	})
	r := server.New(server.Options{})
	ingest.Register(r.Mux(), ingest.Deps{
		Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
		Proxy: engine,
	})
	h.proxyServer.Config.Handler = r.Handler()

	// Client calls OpenAI endpoint, gets translated OpenAI response from Anthropic provider
	t.Run("OpenAIToAnthropic_NonStream", func(t *testing.T) {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"Cross proto test"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.rawKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var res struct {
			Object  string `json:"object"`
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if res.Object != "chat.completion" {
			t.Fatalf("expected object 'chat.completion', got %q", res.Object)
		}
		if len(res.Choices) == 0 || res.Choices[0].Message.Content != "Hello from mock Anthropic" {
			t.Fatalf("unexpected content: %+v", res)
		}
	})

	t.Run("OpenAIToAnthropic_Stream", func(t *testing.T) {
		body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"Cross proto test"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.rawKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var (
			fullText string
			seenDone bool
			scanner  = bufio.NewScanner(resp.Body)
		)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimPrefix(line, "data: ")
				if payload == "[DONE]" {
					seenDone = true
					break
				}
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(payload), &chunk); err == nil {
					if len(chunk.Choices) > 0 {
						fullText += chunk.Choices[0].Delta.Content
					}
				}
			}
		}

		if !seenDone {
			t.Error("expected [DONE] for OpenAI stream client")
		}
		if fullText != "Hello from mock Anthropic" {
			t.Fatalf("expected 'Hello from mock Anthropic', got %q", fullText)
		}
	})

	// Client calls OpenAI endpoint, backend is Gemini
	t.Run("OpenAIToGemini_NonStream", func(t *testing.T) {
		geminiResolver := fallback.StaticResolver{
			Targets: []fallback.Target{
				{
					Provider: client.Provider{
						ID:       "mock-gemini-backend",
						Protocol: domain.ProtocolGemini,
						BaseURL:  h.mockServer.URL,
						APIKey:   "mock-gem-key",
					},
					Model: "gemini-1.5-pro",
				},
			},
		}
		h.proxyServer.Config.Handler = server.New(server.Options{}).Handler()
		r := server.New(server.Options{})
		ingest.Register(r.Mux(), ingest.Deps{
			Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
			Proxy: fallback.NewEngine(fallback.Config{Resolver: geminiResolver, Client: cli}),
		})
		h.proxyServer.Config.Handler = r.Handler()

		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"Cross proto test"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.rawKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var res struct {
			Object  string `json:"object"`
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(res.Choices) == 0 || res.Choices[0].Message.Content != "Hello from mock Gemini" {
			t.Fatalf("unexpected content: %+v", res)
		}
	})

	t.Run("OpenAIToGemini_Stream", func(t *testing.T) {
		geminiResolver := fallback.StaticResolver{
			Targets: []fallback.Target{
				{
					Provider: client.Provider{
						ID:       "mock-gemini-backend",
						Protocol: domain.ProtocolGemini,
						BaseURL:  h.mockServer.URL,
						APIKey:   "mock-gem-key",
					},
					Model: "gemini-1.5-pro",
				},
			},
		}
		r := server.New(server.Options{})
		ingest.Register(r.Mux(), ingest.Deps{
			Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
			Proxy: fallback.NewEngine(fallback.Config{Resolver: geminiResolver, Client: cli}),
		})
		h.proxyServer.Config.Handler = r.Handler()

		body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"Cross proto test"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.rawKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var (
			fullText string
			seenDone bool
			scanner  = bufio.NewScanner(resp.Body)
		)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimPrefix(line, "data: ")
				if payload == "[DONE]" {
					seenDone = true
					break
				}
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(payload), &chunk); err == nil {
					if len(chunk.Choices) > 0 {
						fullText += chunk.Choices[0].Delta.Content
					}
				}
			}
		}

		if !seenDone {
			t.Error("expected [DONE] for OpenAI stream client")
		}
		if fullText != "Hello from mock Gemini" {
			t.Fatalf("expected 'Hello from mock Gemini', got %q", fullText)
		}
	})

	// Client calls Anthropic endpoint, backend is OpenAI
	t.Run("AnthropicToOpenAI_NonStream", func(t *testing.T) {
		openaiResolver := fallback.StaticResolver{
			Targets: []fallback.Target{
				{
					Provider: client.Provider{
						ID:       "mock-openai-backend",
						Protocol: domain.ProtocolOpenAI,
						BaseURL:  h.mockServer.URL,
						APIKey:   "sk-mock",
					},
					Model: "gpt-4o",
				},
			},
		}
		r := server.New(server.Options{})
		ingest.Register(r.Mux(), ingest.Deps{
			Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
			Proxy: fallback.NewEngine(fallback.Config{Resolver: openaiResolver, Client: cli}),
		})
		h.proxyServer.Config.Handler = r.Handler()

		body := `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":"Cross proto test"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", h.rawKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var res struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if res.Type != "message" {
			t.Fatalf("expected type 'message', got %q", res.Type)
		}
		if len(res.Content) == 0 || res.Content[0].Text != "Hello from mock OpenAI" {
			t.Fatalf("unexpected content: %+v", res)
		}
	})

	t.Run("AnthropicToOpenAI_Stream", func(t *testing.T) {
		openaiResolver := fallback.StaticResolver{
			Targets: []fallback.Target{
				{
					Provider: client.Provider{
						ID:       "mock-openai-backend",
						Protocol: domain.ProtocolOpenAI,
						BaseURL:  h.mockServer.URL,
						APIKey:   "sk-mock",
					},
					Model: "gpt-4o",
				},
			},
		}
		r := server.New(server.Options{})
		ingest.Register(r.Mux(), ingest.Deps{
			Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
			Proxy: fallback.NewEngine(fallback.Config{Resolver: openaiResolver, Client: cli}),
		})
		h.proxyServer.Config.Handler = r.Handler()

		body := `{"model":"claude-3-5-sonnet","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Cross proto test"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", h.rawKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var (
			fullText string
			seenStop bool
			scanner  = bufio.NewScanner(resp.Body)
		)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimPrefix(line, "data: ")
				var ev struct {
					Type  string `json:"type"`
					Delta struct {
						Text string `json:"text"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(payload), &ev); err == nil {
					if ev.Type == "content_block_delta" {
						fullText += ev.Delta.Text
					}
					if ev.Type == "message_stop" {
						seenStop = true
					}
				}
			}
		}

		if !seenStop {
			t.Error("expected message_stop event")
		}
		if fullText != "Hello from mock OpenAI" {
			t.Fatalf("expected 'Hello from mock OpenAI', got %q", fullText)
		}
	})
}

// ---------------------------------------------------------------------------
// 3. Fallback Chain: Safe Retry vs Mid-Stream Abort
// ---------------------------------------------------------------------------

func TestE2E_FallbackRetry(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	h := setupE2E(t, nil)
	defer h.Close()

	// Provider 1 fails with 503; Provider 2 succeeds
	resolver := fallback.StaticResolver{
		Targets: []fallback.Target{
			{
				Provider: client.Provider{
					ID:       "failing-target-1",
					Protocol: domain.ProtocolOpenAI,
					BaseURL:  h.mockServer.URL,
					APIKey:   "k1",
					ExtraHeaders: map[string]string{
						"X-Mock-Status": "503",
					},
				},
				Model: "gpt-4o",
			},
			{
				Provider: client.Provider{
					ID:       "healthy-target-2",
					Protocol: domain.ProtocolOpenAI,
					BaseURL:  h.mockServer.URL,
					APIKey:   "k2",
					ExtraHeaders: map[string]string{
						"X-Mock-Echo": "Recovered via target 2",
					},
				},
				Model: "gpt-4o",
			},
		},
	}

	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()
	engine := fallback.NewEngine(fallback.Config{
		Resolver: resolver,
		Client:   cli,
	})
	r := server.New(server.Options{})
	ingest.Register(r.Mux(), ingest.Deps{
		Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
		Proxy: engine,
	})
	h.proxyServer.Config.Handler = r.Handler()

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"Fallback test"}]}`
	req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.rawKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK after fallback, got %d", resp.StatusCode)
	}

	var res struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Choices) == 0 || res.Choices[0].Message.Content != "Recovered via target 2" {
		t.Fatalf("expected recovery content, got %+v", res)
	}
}

// ---------------------------------------------------------------------------
// 4. Error Paths: 400 Bad Request, 401 Unauthorized, 413 Payload Too Large
// ---------------------------------------------------------------------------

func TestE2E_ErrorPaths(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	h := setupE2E(t, nil)
	defer h.Close()

	// Target returns 400
	resolver := fallback.StaticResolver{
		Targets: []fallback.Target{
			{
				Provider: client.Provider{
					ID:       "bad-request-target",
					Protocol: domain.ProtocolOpenAI,
					BaseURL:  h.mockServer.URL,
					APIKey:   "k1",
					ExtraHeaders: map[string]string{
						"X-Mock-Status": "400",
					},
				},
				Model: "gpt-4o",
			},
		},
	}

	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()
	engine := fallback.NewEngine(fallback.Config{
		Resolver:             resolver,
		Client:               cli,
		MaxRequestBodyBytes:  1024, // 1KB cap for 413 test
		MaxResponseBodyBytes: 1024 * 1024,
	})
	r := server.New(server.Options{})
	ingest.Register(r.Mux(), ingest.Deps{
		Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
		Proxy: engine,
	})
	h.proxyServer.Config.Handler = r.Handler()

	// 4a. 401 Unauthorized (missing or forged key)
	t.Run("401_Unauthorized", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer invalid-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	// 4b. 413 Payload Too Large
	t.Run("413_PayloadTooLarge", func(t *testing.T) {
		hugePrompt := strings.Repeat("A", 2048)
		body := fmt.Sprintf(`{"model":"gpt-4o","messages":[{"role":"user","content":%q}]}`, hugePrompt)
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.rawKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d", resp.StatusCode)
		}
	})

	// 4c. Provider 400 Bad Request error propagation
	t.Run("400_ProviderBadRequest", func(t *testing.T) {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"bad request"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.rawKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", resp.StatusCode)
		}
	})
}

// ---------------------------------------------------------------------------
// 5. TTFT Overhead vs Direct Mock Connection (Budget: < 10ms)
// ---------------------------------------------------------------------------

func TestE2E_TTFTOverheadBudget(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	h := setupE2E(t, nil)
	defer h.Close()

	resolver := fallback.StaticResolver{
		Targets: []fallback.Target{
			{
				Provider: client.Provider{
					ID:       "mock-openai",
					Protocol: domain.ProtocolOpenAI,
					BaseURL:  h.mockServer.URL,
					APIKey:   "sk-mock",
				},
				Model: "gpt-4o",
			},
		},
	}
	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()
	engine := fallback.NewEngine(fallback.Config{
		Resolver: resolver,
		Client:   cli,
	})
	r := server.New(server.Options{})
	ingest.Register(r.Mux(), ingest.Deps{
		Auth:  auth.NewVerifier(h.store, []byte("e2e-pepper-secret")),
		Proxy: engine,
	})
	h.proxyServer.Config.Handler = r.Handler()

	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"TTFT measurement"}]}`

	testClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	defer testClient.CloseIdleConnections()

	// Measure Direct TTFT
	measureTTFT := func(targetURL string, authHeader string) time.Duration {
		req, _ := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(body))
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		req.Header.Set("Content-Type", "application/json")

		start := time.Now()
		resp, err := testClient.Do(req)
		if err != nil {
			t.Fatalf("ttft req failed: %v", err)
		}
		defer resp.Body.Close()

		buf := make([]byte, 64)
		_, _ = resp.Body.Read(buf)
		ttft := time.Since(start)
		_, _ = io.Copy(io.Discard, resp.Body)
		return ttft
	}

	// Warmup: establish connection pools for direct and proxied paths
	for i := 0; i < 3; i++ {
		_ = measureTTFT(h.mockServer.URL+"/v1/chat/completions", "")
		_ = measureTTFT(h.proxyServer.URL+"/v1/chat/completions", "Bearer "+h.rawKey)
	}

	// Measure iterations
	var (
		diffs []time.Duration
		iters = 10
	)

	for i := 0; i < iters; i++ {
		d := measureTTFT(h.mockServer.URL+"/v1/chat/completions", "")
		p := measureTTFT(h.proxyServer.URL+"/v1/chat/completions", "Bearer "+h.rawKey)
		diff := p - d
		if diff < 0 {
			diff = 0
		}
		diffs = append(diffs, diff)
	}

	sort.Slice(diffs, func(i, j int) bool { return diffs[i] < diffs[j] })
	medianOverhead := diffs[len(diffs)/2]

	t.Logf("TTFT Median Overhead: %v (samples: %v)", medianOverhead, diffs)

	// Budget specified in tasks/phase-3.proxy-core.graph.yaml: TTFT overhead < 10ms
	const budget = 10 * time.Millisecond
	if medianOverhead > budget {
		t.Fatalf("TTFT overhead %v exceeded budget of %v", medianOverhead, budget)
	}
}
