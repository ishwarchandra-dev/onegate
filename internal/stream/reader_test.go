package stream

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

// chunkReader simulates a transport that returns fixed or variable-sized byte chunks.
type chunkReader struct {
	data      []byte
	chunkSize int
	pos       int
}

func (cr *chunkReader) Read(p []byte) (n int, err error) {
	if cr.pos >= len(cr.data) {
		return 0, io.EOF
	}
	limit := cr.chunkSize
	if limit <= 0 {
		limit = 1
	}
	remaining := len(cr.data) - cr.pos
	if limit > remaining {
		limit = remaining
	}
	if limit > len(p) {
		limit = len(p)
	}
	copy(p, cr.data[cr.pos:cr.pos+limit])
	cr.pos += limit
	return limit, nil
}

func TestReaderBasicFrames(t *testing.T) {
	input := "data: hello world\n\n" +
		": keepalive comment\n\n" +
		"event: custom\ndata: payload line 1\ndata: payload line 2\n\n" +
		"data: [DONE]\n\n"

	r := NewReader(strings.NewReader(input))

	// Frame 1
	f1, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	if f1.Event != "" || f1.Data != "hello world" {
		t.Errorf("frame 1 mismatch: got %+v", f1)
	}

	// Frame 2 (comment skipped, got custom event)
	f2, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	if f2.Event != "custom" || f2.Data != "payload line 1\npayload line 2" {
		t.Errorf("frame 2 mismatch: got %+v", f2)
	}

	// Frame 3 ([DONE])
	f3, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("frame 3: %v", err)
	}
	if !sse.IsDone(f3) {
		t.Errorf("frame 3 not [DONE]: %+v", f3)
	}

	// EOF
	_, err = r.ReadFrame()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestReaderCRLF(t *testing.T) {
	input := "data: windows-style\r\n\r\nevent: ping\r\ndata: pong\r\n\r\n"
	r := NewReader(strings.NewReader(input))

	f1, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	if f1.Data != "windows-style" {
		t.Errorf("frame 1: %q", f1.Data)
	}

	f2, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	if f2.Event != "ping" || f2.Data != "pong" {
		t.Errorf("frame 2: %+v", f2)
	}

	_, err = r.ReadFrame()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestReaderSplitChunks(t *testing.T) {
	raw := "data: chunk 1\n\n" +
		"event: delta\ndata: chunk 2\n\n" +
		"data: {\"id\":\"abc\",\"content\":\"chunk 3\"}\n\n" +
		"data: [DONE]\n\n"

	// Test varying chunk sizes from 1 byte up to 17 bytes to test every boundary.
	for chunkSize := 1; chunkSize <= 17; chunkSize++ {
		t.Run(strings.Repeat(".", chunkSize), func(t *testing.T) {
			cr := &chunkReader{data: []byte(raw), chunkSize: chunkSize}
			r := NewReader(cr)

			f1, err := r.ReadFrame()
			if err != nil {
				t.Fatalf("f1 (chunk %d): %v", chunkSize, err)
			}
			if f1.Data != "chunk 1" {
				t.Errorf("f1 data: %q", f1.Data)
			}

			f2, err := r.ReadFrame()
			if err != nil {
				t.Fatalf("f2 (chunk %d): %v", chunkSize, err)
			}
			if f2.Event != "delta" || f2.Data != "chunk 2" {
				t.Errorf("f2: %+v", f2)
			}

			f3, err := r.ReadFrame()
			if err != nil {
				t.Fatalf("f3 (chunk %d): %v", chunkSize, err)
			}
			if f3.Data != `{"id":"abc","content":"chunk 3"}` {
				t.Errorf("f3: %+v", f3)
			}

			f4, err := r.ReadFrame()
			if err != nil {
				t.Fatalf("f4 (chunk %d): %v", chunkSize, err)
			}
			if !sse.IsDone(f4) {
				t.Errorf("f4 not [DONE]: %+v", f4)
			}

			_, err = r.ReadFrame()
			if !errors.Is(err, io.EOF) {
				t.Fatalf("expected EOF, got %v", err)
			}
		})
	}
}

func TestReaderSplitUTF8Sequences(t *testing.T) {
	// Multilingual text with 2-byte, 3-byte, and 4-byte UTF-8 runes:
	// - "Hello 🌍" (Earth globe: 4-byte 0xF0 0x9F 0x8C 0x8D)
	// - "こんにちは" (Japanese: 3-byte runes)
	// - "مرحبا" (Arabic)
	// - "Русский" (Russian: 2-byte runes)
	// - "Rocket 🚀!" (Rocket emoji: 4-byte 0xF0 0x9F 0x9A 0x80)
	samples := []string{
		"Hello 🌍 World 🚀",
		"日本語テスト：東京タワーへようこそ！",
		"مرحبا بالعالم",
		"Привет мир, тестирование потока данных",
	}

	for _, sample := range samples {
		t.Run(sample, func(t *testing.T) {
			raw := "data: " + sample + "\n\n"
			// Split explicitly at every single byte offset:
			for split := 1; split < len(raw); split++ {
				chunks := [][]byte{
					[]byte(raw[:split]),
					[]byte(raw[split:]),
				}
				cr := &manualChunkReader{chunks: chunks}
				r := NewReader(cr)

				frame, err := r.ReadFrame()
				if err != nil {
					t.Fatalf("split at %d: %v", split, err)
				}
				if frame.Data != sample {
					t.Fatalf("split at %d: want %q, got %q", split, sample, frame.Data)
				}
				if !utf8.ValidString(frame.Data) {
					t.Fatalf("split at %d: invalid UTF-8 string: %x", split, frame.Data)
				}
				if strings.ContainsRune(frame.Data, utf8.RuneError) {
					t.Fatalf("split at %d: contained RuneError: %q", split, frame.Data)
				}

				_, err = r.ReadFrame()
				if !errors.Is(err, io.EOF) {
					t.Fatalf("split at %d: expected EOF, got %v", split, err)
				}
			}
		})
	}
}

func TestReaderMaxFrameSize(t *testing.T) {
	// Frame size limit of 100 bytes.
	oversized := "data: " + strings.Repeat("x", 200) + "\n\n"
	r := NewReaderSize(strings.NewReader(oversized), 100)

	_, err := r.ReadFrame()
	if err == nil {
		t.Fatal("expected error on oversized frame, got nil")
	}
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestReaderTrailingFrameAtEOF(t *testing.T) {
	// Frame without trailing double newline at EOF.
	input := "data: trailing data without final newline"
	r := NewReader(strings.NewReader(input))

	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Data != "trailing data without final newline" {
		t.Fatalf("got %q", f.Data)
	}

	_, err = r.ReadFrame()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

type manualChunkReader struct {
	chunks [][]byte
	idx    int
}

func (m *manualChunkReader) Read(p []byte) (n int, err error) {
	if m.idx >= len(m.chunks) {
		return 0, io.EOF
	}
	chunk := m.chunks[m.idx]
	m.idx++
	copy(p, chunk)
	return len(chunk), nil
}

func TestReaderSplitDelimiters(t *testing.T) {
	// Delimiters split across chunks:
	// Chunk 1: "data: msg\r"
	// Chunk 2: "\n\r"
	// Chunk 3: "\n"
	cr := &manualChunkReader{
		chunks: [][]byte{
			[]byte("data: msg\r"),
			[]byte("\n\r"),
			[]byte("\n"),
		},
	}
	r := NewReader(cr)

	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if f.Data != "msg" {
		t.Fatalf("got %q, want msg", f.Data)
	}

	_, err = r.ReadFrame()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestReaderBufferCompaction(t *testing.T) {
	// Stream many small frames to verify buffer stays bounded and doesn't leak memory.
	var b bytes.Buffer
	for i := 0; i < 5000; i++ {
		b.WriteString("data: tiny\n\n")
	}

	r := NewReader(&b)
	for i := 0; i < 5000; i++ {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if f.Data != "tiny" {
			t.Fatalf("frame %d data: %q", i, f.Data)
		}
	}

	if cap(r.buf) > 65536 {
		t.Fatalf("reader buffer grew excessively: cap=%d", cap(r.buf))
	}
}
