package sse

// Regression tests for parser behaviors pinned by the p8.fuzzing
// campaign. FuzzSplitFrames/1e08851f22a725b1 found that lone-CR line
// endings leaked "\r" into frame data; the parser now normalizes CR,
// LF, and CRLF per the WHATWG SSE spec.
import "testing"

func TestSplitFramesLoneCRTerminators(t *testing.T) {
	// CR-only stream: two frames (blank CR line separates blocks), no CR
	// retained anywhere; consecutive data lines join with "\n" per spec.
	frames := SplitFrames([]byte("data: one\rdata: two\r\rdata: [DONE]\r"))
	want := []Frame{{Data: "one\ntwo"}, {Data: "[DONE]"}}
	if len(frames) != len(want) {
		t.Fatalf("frame count: want %d, got %d (%+v)", len(want), len(frames), frames)
	}
	for i := range want {
		if frames[i] != want[i] {
			t.Fatalf("frame %d: want %+v, got %+v", i, want[i], frames[i])
		}
	}
}

func TestSplitFramesMixedTerminators(t *testing.T) {
	// CRLF, LF, and CR mixed in one body.
	frames := SplitFrames([]byte("event: a\r\ndata: 1\n\rdata: 2\r\n"))
	want := []Frame{{Event: "a", Data: "1"}, {Data: "2"}}
	if len(frames) != len(want) {
		t.Fatalf("frame count: want %d, got %d (%+v)", len(want), len(frames), frames)
	}
	for i := range want {
		if frames[i] != want[i] {
			t.Fatalf("frame %d: want %+v, got %+v", i, want[i], frames[i])
		}
	}
}

func TestSplitFramesBareCRIgnoredAsLineFeed(t *testing.T) {
	// "data:\r" alone: one empty-data frame, no CR in any field
	// (this is the exact fuzzer crash input, minimized).
	frames := SplitFrames([]byte("data:\r"))
	if len(frames) != 1 || frames[0].Data != "" || frames[0].Event != "" {
		t.Fatalf("want one empty frame, got %+v", frames)
	}
}
