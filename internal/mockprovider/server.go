// Package mockprovider provides a mock upstream LLM provider supporting
// OpenAI, Anthropic, and Gemini wire protocols in both streaming and
// buffered modes.
package mockprovider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"
)

// Server is a test server running the mock provider handler.
type Server struct {
	*httptest.Server
}

// NewServer starts a new httptest server with the mock provider handler.
func NewServer() *Server {
	srv := httptest.NewServer(NewHandler())
	return &Server{Server: srv}
}

// NewHandler returns an http.Handler that mocks OpenAI, Anthropic, and Gemini endpoints.
func NewHandler() http.Handler {
	mux := http.NewServeMux()

	// OpenAI
	mux.HandleFunc("POST /v1/chat/completions", handleOpenAI)
	mux.HandleFunc("POST /chat/completions", handleOpenAI)

	// Anthropic
	mux.HandleFunc("POST /v1/messages", handleAnthropic)

	// Gemini
	mux.HandleFunc("POST /v1beta/models/{target...}", handleGemini)

	return mux
}

func handleGemini(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("target")
	if strings.Contains(target, "streamGenerateContent") {
		handleGeminiStream(w, r)
	} else {
		handleGeminiNonStream(w, r)
	}
}

// checkMockOverrides inspects headers for simulated delays, statuses, or aborts.
func checkMockOverrides(w http.ResponseWriter, r *http.Request, protocol string) bool {
	if delayMS := r.Header.Get("X-Mock-Delay"); delayMS != "" {
		if d, err := strconv.Atoi(delayMS); err == nil && d > 0 {
			select {
			case <-time.After(time.Duration(d) * time.Millisecond):
			case <-r.Context().Done():
				return true
			}
		}
	}

	if statusStr := r.Header.Get("X-Mock-Status"); statusStr != "" {
		if code, err := strconv.Atoi(statusStr); err == nil && code >= 400 {
			renderMockError(w, protocol, code)
			return true
		}
	}

	return false
}

func renderMockError(w http.ResponseWriter, protocol string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	switch protocol {
	case "anthropic":
		_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"api_error","message":"mock anthropic error %d"}}`, status)
	case "gemini":
		_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"message":"mock gemini error %d","status":"INTERNAL"}}`, status, status)
	default: // openai
		_, _ = fmt.Fprintf(w, `{"error":{"message":"mock openai error %d","type":"api_error","code":"mock_error"}}`, status)
	}
}

func handleOpenAI(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if checkMockOverrides(w, r, "openai") {
		return
	}

	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(bodyBytes, &req)

	model := req.Model
	if model == "" {
		model = "gpt-4o"
	}

	if sc, ok := parseScenario(model); ok {
		if req.Stream {
			if applyStreamScenario(w, r, "openai", sc) {
				return
			}
		} else if applyScenario(w, r, "openai", model, sc) {
			return
		}
	}

	echoText := "Hello from mock OpenAI"
	if custom := r.Header.Get("X-Mock-Echo"); custom != "" {
		echoText = custom
	}

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Frame 1: role
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n", model)
		flusher.Flush()

		if abort := r.Header.Get("X-Mock-Abort-After"); abort == "1" {
			return
		}

		// Frame 2: content delta
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", model, echoText)
		flusher.Flush()

		// Frame 3: finish & usage
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n", model)
		flusher.Flush()

		// Frame 4: [DONE]
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	// Buffered / non-streaming
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{
  "id": "chatcmpl-mock",
  "object": "chat.completion",
  "created": 1700000000,
  "model": %q,
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": %q
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 5,
    "total_tokens": 15
  }
}`, model, echoText)
}

func handleAnthropic(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(bodyBytes, &req)

	model := req.Model
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}

	if sc, ok := parseScenario(model); ok {
		if req.Stream {
			if applyStreamScenario(w, r, "anthropic", sc) {
				return
			}
		} else if applyScenario(w, r, "anthropic", model, sc) {
			return
		}
	}

	if checkMockOverrides(w, r, "anthropic") {
		return
	}

	echoText := "Hello from mock Anthropic"
	if custom := r.Header.Get("X-Mock-Echo"); custom != "" {
		echoText = custom
	}

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// event: message_start
		_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_mock\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":%q,\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\n", model)
		flusher.Flush()

		if abort := r.Header.Get("X-Mock-Abort-After"); abort == "1" {
			return
		}

		// event: content_block_start
		_, _ = fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		flusher.Flush()

		// event: content_block_delta
		_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", echoText)
		flusher.Flush()

		// event: content_block_stop
		_, _ = fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		flusher.Flush()

		// event: message_delta
		_, _ = fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":6}}\n\n")
		flusher.Flush()

		// event: message_stop
		_, _ = fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
		return
	}

	// Buffered / non-streaming
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{
  "id": "msg_mock",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": %q
    }
  ],
  "model": %q,
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 12,
    "output_tokens": 6
  }
}`, echoText, model)
}

func handleGeminiNonStream(w http.ResponseWriter, r *http.Request) {
	_, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	model := modelFromTarget(r.PathValue("target"))
	if model == "" {
		model = "gemini-1.5-pro"
	}

	if sc, ok := parseScenario(model); ok {
		if applyScenario(w, r, "gemini", model, sc) {
			return
		}
	}

	if checkMockOverrides(w, r, "gemini") {
		return
	}

	echoText := "Hello from mock Gemini"
	if custom := r.Header.Get("X-Mock-Echo"); custom != "" {
		echoText = custom
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {
            "text": %q
          }
        ]
      },
      "finishReason": "STOP",
      "index": 0
    }
  ],
  "usageMetadata": {
    "promptTokenCount": 8,
    "candidatesTokenCount": 4,
    "totalTokenCount": 12
  },
  "modelVersion": %q
}`, echoText, model)
}

func handleGeminiStream(w http.ResponseWriter, r *http.Request) {
	_, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	model := modelFromTarget(r.PathValue("target"))
	if model == "" {
		model = "gemini-1.5-pro"
	}

	if sc, ok := parseScenario(model); ok {
		if applyStreamScenario(w, r, "gemini", sc) {
			return
		}
	}

	if checkMockOverrides(w, r, "gemini") {
		return
	}

	echoText := "Hello from mock Gemini"
	if custom := r.Header.Get("X-Mock-Echo"); custom != "" {
		echoText = custom
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Chunk 1: content
	_, _ = fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":%q}]},\"index\":0}],\"modelVersion\":%q}\n\n", echoText, model)
	flusher.Flush()

	if abort := r.Header.Get("X-Mock-Abort-After"); abort == "1" {
		return
	}

	// Chunk 2: finishReason + usageMetadata
	_, _ = fmt.Fprintf(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":4,\"totalTokenCount\":12},\"modelVersion\":%q}\n\n", model)
	flusher.Flush()
}
