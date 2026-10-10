package anthropic

// p8.fuzzing: Anthropic adapter parser fuzz targets (request, response,
// error envelope, stream event machine). Arbitrary bytes must not
// panic; successful decodes must re-encode.
import (
	"encoding/json"
	"testing"
)

func FuzzDecodeRequest(f *testing.F) {
	seeds := []string{
		`{"model":"claude-3-5-sonnet-latest","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"claude-3","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":"be brief"}`,
		`{"model":"claude-3","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}]}`,
		`{"model":"claude-3","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]}]}`,
		`{"model":"claude-3","max_tokens":10,"messages":[],"tools":[{"name":"f","input_schema":{}}]}`,
		`{"max_tokens":-1}`,
		`{"messages":"x"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := DecodeRequest(body)
		if err != nil {
			return
		}
		if _, err := EncodeRequest(req); err != nil {
			t.Fatalf("decode accepted input but encode failed: %v (req=%+v)", err, req)
		}
	})
}

func FuzzDecodeResponse(f *testing.F) {
	seeds := []string{
		`{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-3","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`,
		`{"content":[{"type":"tool_use","id":"t","name":"f","input":{"a":1}}],"stop_reason":"tool_use","usage":{"input_tokens":-9}}`,
		`{"content":[],"stop_reason":"max_tokens","usage":{}}`,
		`{}`,
		`{"content":"no"}`,
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
		`{"type":"error","error":{"type":"invalid_request_error","message":"m"}}`,
		`{"type":"error","error":{"type":"overloaded_error","message":"m"}}`,
		`{"type":"error","error":{"type":"rate_limit_error","message":"m"}}`,
		`{"error":{"type":"api_error","message":"m"}}`,
		`{"type":"error","error":"flat"}`,
		`null`,
		`<html>502</html>`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, status := range []int{400, 401, 403, 404, 408, 413, 422, 429, 500, 502, 503, 529} {
			ge := DecodeError(body, status)
			out, encStatus := EncodeError(ge)
			var probe map[string]any
			if err := json.Unmarshal(out, &probe); err != nil {
				t.Fatalf("EncodeError produced invalid JSON for status %d: %v (%s)", status, err, out)
			}
			if encStatus < 400 || encStatus > 599 {
				t.Fatalf("EncodeError status %d out of range (input %d)", encStatus, status)
			}
		}
	})
}

func FuzzStreamDecoder(f *testing.F) {
	seeds := []string{
		`{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":3,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
		`{"type":"ping"}`,
		`{"type":"error","error":{"type":"overloaded_error","message":"m"}}`,
		`{"type":123}`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		d := NewStreamDecoder()
		if _, err := d.Decode(body); err == nil {
			// Terminal events must not corrupt later decodes.
			_, _ = d.Decode(body)
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
