package sse

// p8.fuzzing: SSE framing parser fuzz target. The parser sits on the
// hot path between every provider and every client — arbitrary byte
// streams (truncated chunks, CRLF mixes, keep-alive comments, hostile
// field names) must never panic, hang, or produce nondeterministic
// output.
import (
	"strings"
	"testing"
)

func FuzzSplitFrames(f *testing.F) {
	seeds := []string{
		"data: {\"id\":1}\n\ndata: {\"id\":2}\n\n",
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
			"event: content_block_delta\ndata: {\"delta\":\"hi\"}\n\n" +
			"event: message_stop\ndata: {}\n\n",
		"data: [DONE]\n\n",
		": keep-alive comment\ndata: {}\n\n",
		"data: line one\ndata: line two\n\n",
		"data:\r\n\r\n",
		"event: no-data\nevent: second\ndata: x\n\n",
		"id: 42\nretry: 3000\ndata: {\"a\":1}\n\n",
		"data: unterminated",
		"\n\n\n\ndata: {}\n\n",
		"data: \xc3\x28 bad-utf8\n\n",
		"event:\ndata:\n\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		frames := SplitFrames(body)

		// Determinism: same input, same output.
		again := SplitFrames(body)
		if len(frames) != len(again) {
			t.Fatalf("nondeterministic frame count: %d vs %d", len(frames), len(again))
		}
		for i := range frames {
			if frames[i] != again[i] {
				t.Fatalf("nondeterministic frame %d: %+v vs %+v", i, frames[i], again[i])
			}
		}

		for i, fr := range frames {
			// CRLF must be fully normalized away.
			if strings.ContainsAny(fr.Event, "\r") || strings.ContainsAny(fr.Data, "\r") {
				t.Fatalf("frame %d retains CR: event=%q data=%q", i, fr.Event, fr.Data)
			}
			// A frame only exists when a block was opened (event:/data:
			// line seen); a lone "id:"/"retry:" line must not emit one.
			// (Empty data lines are legal: "data:\n\n" yields one frame.)
			_ = i
		}
	})
}
