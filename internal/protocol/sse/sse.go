// Package sse provides minimal server-sent-events frame handling shared by
// the protocol adapters and (later) the streaming pipeline.
//
// A frame is one SSE "event block":
//
//	event: content_block_delta   ← optional (Anthropic uses event names, OpenAI does not)
//	data: {...}
//
// The parser only understands what the adapters emit and consume: frames
// separated by blank lines, "data:" and "event:" lines, and comment lines
// starting with ':' (keep-alives) which are skipped. Multi-line data is
// joined with "\n" per the SSE spec.
package sse

import "strings"

// Frame is one parsed SSE event block.
type Frame struct {
	Event string // "" when the frame carries no event: line (OpenAI style)
	Data  string // may be the literal "[DONE]"
}

// SplitFrames parses an SSE body into frames, dropping keep-alive
// comments. It is deliberately forgiving: CRLF is normalized, stray
// partial blocks at EOF are ignored.
func SplitFrames(body []byte) []Frame {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var frames []Frame
	var event, data strings.Builder
	inBlock := false

	flush := func() {
		if !inBlock {
			return
		}
		frames = append(frames, Frame{Event: event.String(), Data: data.String()})
		event.Reset()
		data.Reset()
		inBlock = false
	}

	for _, line := range strings.Split(text, "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
			// keep-alive comment; skip
		case strings.HasPrefix(line, "event:"):
			inBlock = true
			event.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " "))
		case strings.HasPrefix(line, "data:"):
			inBlock = true
			if data.Len() > 0 {
				data.WriteString("\n")
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// Unknown field (id:, retry:): ignored per spec.
		}
	}
	flush()
	return frames
}

// Format renders a frame in wire form, including the trailing blank line.
func Format(f Frame) string {
	var b strings.Builder
	if f.Event != "" {
		b.WriteString("event: ")
		b.WriteString(f.Event)
		b.WriteString("\n")
	}
	b.WriteString("data: ")
	b.WriteString(f.Data)
	b.WriteString("\n\n")
	return b.String()
}

// FormatAll renders frames back-to-back.
func FormatAll(frames []Frame) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString(Format(f))
	}
	return b.String()
}

// IsDone reports whether a frame's data is the OpenAI stream terminator.
func IsDone(f Frame) bool { return f.Data == "[DONE]" }
