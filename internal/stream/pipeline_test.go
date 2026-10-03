package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

func loadFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "protocol", path))
	if err != nil {
		t.Fatalf("load fixture %s: %v", path, err)
	}
	return b
}

func TestPipelineCrossProtocol(t *testing.T) {
	openAIFixture := loadFixture(t, "openai/testdata/stream_text.sse")
	anthropicFixture := loadFixture(t, "anthropic/testdata/stream_thinking.sse")
	geminiFixture := loadFixture(t, "gemini/testdata/stream_text.sse")

	tests := []struct {
		name          string
		providerProto domain.ProviderProtocol
		clientProto   domain.ProviderProtocol
		input         []byte
		wantClient    string // substring expected in client output
		minFramesOut  int
		wantUsage     bool
	}{
		{
			name:          "OpenAI -> Anthropic",
			providerProto: domain.ProtocolOpenAI,
			clientProto:   domain.ProtocolAnthropic,
			input:         openAIFixture,
			wantClient:    "content_block_delta",
			minFramesOut:  3,
			wantUsage:     true,
		},
		{
			name:          "OpenAI -> Gemini",
			providerProto: domain.ProtocolOpenAI,
			clientProto:   domain.ProtocolGemini,
			input:         openAIFixture,
			wantClient:    "candidates",
			minFramesOut:  2,
			wantUsage:     true,
		},
		{
			name:          "Anthropic -> OpenAI",
			providerProto: domain.ProtocolAnthropic,
			clientProto:   domain.ProtocolOpenAI,
			input:         anthropicFixture,
			wantClient:    "chat.completion.chunk",
			minFramesOut:  3,
			wantUsage:     true,
		},
		{
			name:          "Anthropic -> Gemini",
			providerProto: domain.ProtocolAnthropic,
			clientProto:   domain.ProtocolGemini,
			input:         anthropicFixture,
			wantClient:    "candidates",
			minFramesOut:  2,
			wantUsage:     true,
		},
		{
			name:          "Gemini -> OpenAI",
			providerProto: domain.ProtocolGemini,
			clientProto:   domain.ProtocolOpenAI,
			input:         geminiFixture,
			wantClient:    "chat.completion.chunk",
			minFramesOut:  3,
			wantUsage:     true,
		},
		{
			name:          "Gemini -> Anthropic",
			providerProto: domain.ProtocolGemini,
			clientProto:   domain.ProtocolAnthropic,
			input:         geminiFixture,
			wantClient:    "content_block_delta",
			minFramesOut:  3,
			wantUsage:     true,
		},
		{
			name:          "OpenAI -> OpenAI",
			providerProto: domain.ProtocolOpenAI,
			clientProto:   domain.ProtocolOpenAI,
			input:         openAIFixture,
			wantClient:    "[DONE]",
			minFramesOut:  3,
			wantUsage:     true,
		},
		{
			name:          "Anthropic -> Anthropic",
			providerProto: domain.ProtocolAnthropic,
			clientProto:   domain.ProtocolAnthropic,
			input:         anthropicFixture,
			wantClient:    "message_stop",
			minFramesOut:  3,
			wantUsage:     true,
		},
		{
			name:          "Gemini -> Gemini",
			providerProto: domain.ProtocolGemini,
			clientProto:   domain.ProtocolGemini,
			input:         geminiFixture,
			wantClient:    "candidates",
			minFramesOut:  2,
			wantUsage:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			w := NewCustomWriter(&out, nil)

			var capturedEvents []domain.StreamEvent
			summary, err := Execute(context.Background(), Config{
				ProviderProto: tc.providerProto,
				ClientProto:   tc.clientProto,
				Upstream:      bytes.NewReader(tc.input),
				Destination:   w,
				OnEvent: func(ev domain.StreamEvent) {
					capturedEvents = append(capturedEvents, ev)
				},
				OverrideModel: "custom-routed-model",
			})
			if err != nil {
				t.Fatalf("execute failed: %v", err)
			}

			if summary.FramesOut < tc.minFramesOut {
				t.Errorf("frames out: got %d, want >= %d", summary.FramesOut, tc.minFramesOut)
			}
			if summary.FirstTokenAt <= 0 {
				t.Errorf("expected positive FirstTokenAt, got %v", summary.FirstTokenAt)
			}
			if summary.Model != "custom-routed-model" {
				t.Errorf("model override: got %q, want custom-routed-model", summary.Model)
			}

			outputStr := out.String()
			if !strings.Contains(outputStr, tc.wantClient) {
				t.Errorf("missing expected client substring %q in output:\n%s", tc.wantClient, outputStr)
			}

			if tc.wantUsage && summary.Usage.TotalTokens == 0 {
				t.Errorf("expected non-zero total tokens, got %+v", summary.Usage)
			}

			if len(capturedEvents) == 0 {
				t.Error("expected capturedEvents to be non-empty")
			}
		})
	}
}

type errAfterReader struct {
	data     []byte
	errAfter int
	read     int
	errToRet error
}

func (e *errAfterReader) Read(p []byte) (n int, err error) {
	if e.read >= e.errAfter {
		return 0, e.errToRet
	}
	remaining := e.errAfter - e.read
	limit := len(p)
	if limit > remaining {
		limit = remaining
	}
	copy(p, e.data[e.read:e.read+limit])
	e.read += limit
	return limit, nil
}

func TestPipelinePreFirstByteFailureAllowsFallback(t *testing.T) {
	// Upstream errors before any complete frame can be parsed.
	brokenReader := &errAfterReader{
		data:     []byte("data: partial broken without end"),
		errAfter: 10,
		errToRet: errors.New("upstream connection reset"),
	}

	var out bytes.Buffer
	w := NewCustomWriter(&out, nil)

	summary, err := Execute(context.Background(), Config{
		ProviderProto: domain.ProtocolOpenAI,
		ClientProto:   domain.ProtocolOpenAI,
		Upstream:      brokenReader,
		Destination:   w,
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Critical guarantee: Destination.Written() must be false!
	if w.Written() {
		t.Fatal("destination written must be FALSE before first byte, allowing fallback")
	}
	if out.Len() > 0 {
		t.Fatalf("expected 0 bytes written to destination, got %d", out.Len())
	}
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
}

func TestPipelinePostFirstByteEmitsClientError(t *testing.T) {
	// Upstream succeeds for first frame, then fails mid-stream.
	raw := "data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"created\":1770000000,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token1\"}}]}\n\n" +
		"data: broken..."
	brokenReader := &errAfterReader{
		data:     []byte(raw),
		errAfter: len(raw) - 5,
		errToRet: io.ErrUnexpectedEOF,
	}

	var out bytes.Buffer
	w := NewCustomWriter(&out, nil)

	_, err := Execute(context.Background(), Config{
		ProviderProto: domain.ProtocolOpenAI,
		ClientProto:   domain.ProtocolOpenAI,
		Upstream:      brokenReader,
		Destination:   w,
	})

	if err == nil {
		t.Fatal("expected mid-stream error, got nil")
	}
	if !w.Written() {
		t.Fatal("destination written should be true since first token was emitted")
	}

	// Should contain error frame in OpenAI format.
	output := out.String()
	if !strings.Contains(output, "token1") {
		t.Errorf("missing token1 in output: %s", output)
	}
	if !strings.Contains(output, "error") {
		t.Errorf("missing error envelope in mid-stream client output: %s", output)
	}
}

func TestPipelineContextCancellation(t *testing.T) {
	pr, pw := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())

	var out bytes.Buffer
	w := NewCustomWriter(&out, nil)

	var wg sync.WaitGroup
	wg.Add(1)

	var (
		summary *Summary
		execErr error
	)

	go func() {
		defer wg.Done()
		summary, execErr = Execute(ctx, Config{
			ProviderProto: domain.ProtocolOpenAI,
			ClientProto:   domain.ProtocolOpenAI,
			Upstream:      pr,
			Destination:   w,
		})
	}()

	// Write first chunk.
	_, _ = pw.Write([]byte("data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"created\":1770000000,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n"))

	// Give pipeline time to process first chunk.
	time.Sleep(10 * time.Millisecond)

	// Client cancels stream.
	cancel()

	// Unblock pipe.
	_ = pw.Close()
	wg.Wait()

	if !errors.Is(execErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", execErr)
	}
	if summary == nil || !summary.Cancelled {
		t.Fatalf("expected summary.Cancelled=true, got %+v", summary)
	}
}

func TestPipelineConcurrentStreams(t *testing.T) {
	openAIFixture := loadFixture(t, "openai/testdata/stream_text.sse")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			var out bytes.Buffer
			w := NewCustomWriter(&out, nil)

			s, err := Execute(context.Background(), Config{
				ProviderProto: domain.ProtocolOpenAI,
				ClientProto:   domain.ProtocolAnthropic,
				Upstream:      bytes.NewReader(openAIFixture),
				Destination:   w,
			})
			if err != nil {
				t.Errorf("worker %d: %v", id, err)
				return
			}
			if s.FramesOut == 0 {
				t.Errorf("worker %d: no frames out", id)
			}
		}(i)
	}
	wg.Wait()
}
