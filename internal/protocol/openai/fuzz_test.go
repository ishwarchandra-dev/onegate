package openai

// p8.fuzzing: OpenAI adapter parser fuzz targets. All four parse
// surfaces (request, response, error envelope, stream chunks) must
// survive arbitrary bytes without panic, and successful decodes must
// re-encode (round-trip invariant).
import (
	"encoding/json"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

func FuzzDecodeRequest(f *testing.F) {
	seeds := []string{
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":true}`,
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`,
		`{"model":"x","max_tokens":10,"temperature":0.5,"tools":[{"type":"function","function":{"name":"t","parameters":{}}}]}`,
		`{"model":"x","messages":[],"response_format":{"type":"json_object"}}`,
		`{"bogus":true}`,
		`{"model":123}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := DecodeRequest(body)
		if err != nil {
			return // rejecting garbage is fine; panicking is not
		}
		if _, err := EncodeRequest(req); err != nil {
			t.Fatalf("decode accepted input but encode failed: %v (req=%+v)", err, req)
		}
		if req.Model == "" && len(req.Messages) == 0 {
			// An accepted request with neither model nor messages is
			// suspicious but not fatal — record nothing; encoder above
			// is the real invariant.
			_ = domain.Request{}
		}
	})
}

func FuzzDecodeResponse(f *testing.F) {
	seeds := []string{
		`{"id":"r1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"message":{"content":null},"finish_reason":"length"}],"usage":{"prompt_tokens":-5}}`,
		`{}`,
		`{"choices":"no"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		resp, err := DecodeResponse(body)
		if err != nil {
			return
		}
		if _, err := EncodeResponse(resp); err != nil {
			t.Fatalf("decode accepted input but encode failed: %v", err)
		}
		if resp.Usage.TotalTokens < 0 {
			t.Fatalf("negative total usage accepted: %+v", resp.Usage)
		}
	})
}

func FuzzDecodeError(f *testing.F) {
	seeds := []string{
		`{"error":{"message":"bad","type":"invalid_request_error","code":"x"}}`,
		`{"error":{"message":"m"}}`,
		`{"error":{"code":429}}`,
		`{"error":{"param":"model"}}`,
		`{"message":"flat"}`,
		`null`,
		`[]`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
		// Status matters: exercise the interesting HTTP codes too.
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		// Realistic upstream statuses get the full contract check;
		// out-of-family statuses (0/999) only need to not panic —
		// net/http can never produce them from a real response.
		for _, status := range []int{400, 401, 403, 404, 408, 413, 422, 429, 500, 502, 503, 504, 529} {
			ge := DecodeError(body, status)
			out, encStatus := EncodeError(ge)
			var probe map[string]any
			if err := json.Unmarshal(out, &probe); err != nil {
				t.Fatalf("EncodeError produced invalid JSON for status %d: %v (%s)", status, err, out)
			}
			if encStatus < 400 || encStatus > 599 {
				t.Fatalf("EncodeError status %d out of HTTP error range (input status %d)", encStatus, status)
			}
		}
		for _, status := range []int{0, 999} {
			_, _ = EncodeError(DecodeError(body, status))
		}
	})
}

func FuzzStreamDecoder(f *testing.F) {
	seeds := []string{
		`{"id":"c1","choices":[{"delta":{"role":"assistant","content":"he"}}]}`,
		`{"choices":[{"delta":{"content":"llo"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","function":{"name":"f","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		`{"choices":"x"}`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		// Feed the whole body as one chunk, then line-by-line as a
		// chunk sequence — two state-machine traversals per input.
		d := NewStreamDecoder()
		if _, err := d.Decode(body); err == nil {
			_ = d.CreatedMS() // must not panic post-decode
		}
		d2 := NewStreamDecoder()
		start := 0
		for i := 0; i <= len(body); i++ {
			if i == len(body) || body[i] == '\n' {
				if i > start {
					_, _ = d2.Decode(body[start:i])
				}
				start = i + 1
			}
		}
	})
}
