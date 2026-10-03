// Package stream implements the server-sent-events streaming pipeline
// between providers and clients for OneGate (ADR 004, Phase 3).
//
// Layering contract:
//   - Package stream imports standard library, internal/domain,
//     internal/protocol/sse, and sibling internal/protocol/* adapters.
//   - It never imports server, routing, storage, or config.
package stream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

// DefaultMaxFrameSize limits the maximum buffered bytes for a single SSE
// frame before ErrFrameTooLarge is returned (16 MiB).
const DefaultMaxFrameSize = 16 << 20

// ErrFrameTooLarge indicates an SSE frame exceeded the configured size cap
// without encountering an end-of-frame delimiter.
var ErrFrameTooLarge = errors.New("stream: sse frame exceeds maximum allowed size")

// Reader reads SSE frames from an underlying io.Reader. It correctly
// reconstructs frames split across arbitrary chunk boundaries, handles
// CRLF, LF, and CR delimiters, skips keep-alive comments, and preserves
// multi-byte UTF-8 sequences across chunk splits.
type Reader struct {
	r            io.Reader
	maxFrameSize int

	buf     []byte
	readBuf []byte
	eof     bool
	err     error
}

// NewReader returns a Reader with DefaultMaxFrameSize.
func NewReader(r io.Reader) *Reader {
	return NewReaderSize(r, DefaultMaxFrameSize)
}

// NewReaderSize returns a Reader with a custom maximum frame size.
func NewReaderSize(r io.Reader, maxFrameSize int) *Reader {
	if maxFrameSize <= 0 {
		maxFrameSize = DefaultMaxFrameSize
	}
	return &Reader{
		r:            r,
		maxFrameSize: maxFrameSize,
		buf:          make([]byte, 0, 4096),
		readBuf:      make([]byte, 4096),
	}
}

// ReadFrame reads and returns the next complete SSE frame.
// When the underlying stream ends cleanly, it returns io.EOF.
func (r *Reader) ReadFrame() (sse.Frame, error) {
	var (
		event   strings.Builder
		data    strings.Builder
		inBlock bool
	)

	for {
		// 1. Try to scan complete lines from the buffer.
		for len(r.buf) > 0 {
			advance, line, found := scanLine(r.buf, r.eof)
			if !found {
				break
			}
			r.buf = r.buf[advance:]
			if len(r.buf) == 0 {
				r.buf = r.buf[:0]
			}

			if len(line) == 0 {
				// Empty line terminates an event block if one was started.
				if inBlock {
					return sse.Frame{Event: event.String(), Data: data.String()}, nil
				}
				// Otherwise, skip redundant empty lines.
				continue
			}

			if len(line) > r.maxFrameSize {
				return sse.Frame{}, fmt.Errorf("%w: line size %d exceeds limit %d", ErrFrameTooLarge, len(line), r.maxFrameSize)
			}

			// Comment line: starts with ':' (e.g. keepalive comments).
			if line[0] == ':' {
				continue
			}

			// Data line: starts with "data:".
			if bytes.HasPrefix(line, []byte("data:")) {
				inBlock = true
				val := bytes.TrimPrefix(line[5:], []byte(" "))
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.Write(val)
				if data.Len()+event.Len() > r.maxFrameSize {
					return sse.Frame{}, fmt.Errorf("%w: frame content exceeds limit %d", ErrFrameTooLarge, r.maxFrameSize)
				}
				continue
			}

			// Event name: starts with "event:".
			if bytes.HasPrefix(line, []byte("event:")) {
				inBlock = true
				val := bytes.TrimPrefix(line[6:], []byte(" "))
				event.Write(val)
				if data.Len()+event.Len() > r.maxFrameSize {
					return sse.Frame{}, fmt.Errorf("%w: frame content exceeds limit %d", ErrFrameTooLarge, r.maxFrameSize)
				}
				continue
			}

			// Other SSE fields (id:, retry:) are ignored per spec.
		}

		// 2. If at EOF and an incomplete block remains, flush it.
		if r.eof {
			if inBlock {
				return sse.Frame{Event: event.String(), Data: data.String()}, nil
			}
			return sse.Frame{}, io.EOF
		}

		// 3. Return any sticky read error before pulling more data.
		if r.err != nil {
			if inBlock {
				return sse.Frame{Event: event.String(), Data: data.String()}, nil
			}
			return sse.Frame{}, r.err
		}

		// 4. Guard against unbounded frame size.
		if len(r.buf) >= r.maxFrameSize {
			return sse.Frame{}, fmt.Errorf("%w (%d bytes)", ErrFrameTooLarge, len(r.buf))
		}

		// 5. Compact buffer if needed before appending new data.
		if len(r.buf) > 0 && cap(r.buf) > 32768 && len(r.buf) < cap(r.buf)/4 {
			compact := make([]byte, len(r.buf), len(r.buf)*2)
			copy(compact, r.buf)
			r.buf = compact
		}

		// 6. Read more bytes from the underlying reader.
		n, err := r.r.Read(r.readBuf)
		if n > 0 {
			r.buf = append(r.buf, r.readBuf[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.eof = true
			} else {
				r.err = err
			}
		}
	}
}

// Close closes the underlying reader if it implements io.Closer.
func (r *Reader) Close() error {
	if c, ok := r.r.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// scanLine searches for a line delimiter (\r\n, \n, or standalone \r).
// It returns the number of bytes to advance, the line content (excluding
// delimiter), and whether a line was found.
func scanLine(buf []byte, atEOF bool) (advance int, line []byte, found bool) {
	for i := 0; i < len(buf); i++ {
		b := buf[i]
		if b == '\n' {
			if i > 0 && buf[i-1] == '\r' {
				return i + 1, buf[:i-1], true
			}
			return i + 1, buf[:i], true
		}
		if b == '\r' {
			if i+1 < len(buf) {
				if buf[i+1] == '\n' {
					return i + 2, buf[:i], true
				}
				// Standalone \r followed by non-\n
				return i + 1, buf[:i], true
			}
			// \r is the last byte in buffer.
			if atEOF {
				return i + 1, buf[:i], true
			}
			// Wait for next byte to see if \n follows.
			return 0, nil, false
		}
	}

	if atEOF && len(buf) > 0 {
		return len(buf), buf, true
	}
	return 0, nil, false
}
