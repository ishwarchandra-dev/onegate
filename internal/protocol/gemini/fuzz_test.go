package gemini

// p8.fuzzing: Gemini adapter parser fuzz targets (request, response,
// error envelope, stream chunk machine). Arbitrary bytes must not
// panic; successful decodes must re-encode.
import (
	"encoding/json"
	"testing"
)

func FuzzDecodeRequest(f *testing.F) {
	seeds := []string{
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
		`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"aGk="}}]}]}`,
		`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{"a":1}}}]}]}`,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be brief"}]}}`,
		`{"contents":[{"role":"user","parts":[{"text":"x"}]}],"generationConfig":{"maxOutputTokens":10,"temperature":0.5},"tools":[{"functionDeclarations":[{"name":"f"}]}]}`,
		`{"contents":"no"}`,
		`{}`,
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
		`{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{}}}],"role":"model"},"finishReason":"STOP"}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"a"},{"text":"b"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":-7}}`,
		`{"promptFeedback":{"blockReason":"SAFETY"}}`,
		`{}`,
		`{"candidates":null}`,
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
		`{"error":{"code":400,"message":"m","status":"INVALID_ARGUMENT"}}`,
		`{"error":{"code":429,"message":"m","status":"RESOURCE_EXHAUSTED"}}`,
		`{"error":{"code":503,"message":"m","status":"UNAVAILABLE"}}`,
		`{"error":{"message":"m"}}`,
		`{"error":"flat"}`,
		`null`,
		`<html>upstream</html>`,
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
		`{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{}}}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":5}}`,
		`{"candidates":[{"finishReason":"MAX_TOKENS"}]}`,
		`{"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
		`{"candidates":[]}`,
		`{}`,
		`{"candidates":[{"content":{"parts":"no"}}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		d := NewStreamDecoder()
		if _, err := d.Decode(body); err == nil {
			_ = d.Finish() // must not panic after any decode
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
		_ = d2.Finish()
	})
}
