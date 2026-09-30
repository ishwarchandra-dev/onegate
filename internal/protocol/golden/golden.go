// Package golden provides JSON normalization for byte-stable golden
// round-trip tests (ADR 004: decode→encode cycles must be stable under
// JSON-key normalization).
package golden

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// NormalizeJSON unmarshals arbitrary JSON and re-marshals it with Go's
// deterministic map ordering (keys sorted lexicographically). Two JSON
// documents are semantically equal iff their normalizations are equal.
// Input may be a bare scalar, object, or array.
func NormalizeJSON(b []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("golden: invalid JSON: %w", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Diff returns a human-readable failure message when want and got
// normalizations differ.
func Diff(want, got []byte) string {
	wn, err := NormalizeJSON(want)
	if err != nil {
		return fmt.Sprintf("want is not valid JSON: %v", err)
	}
	gn, err := NormalizeJSON(got)
	if err != nil {
		return fmt.Sprintf("got is not valid JSON: %v", err)
	}
	if bytes.Equal(wn, gn) {
		return ""
	}
	return fmt.Sprintf("normalized mismatch:\n want: %s\n  got: %s", wn, gn)
}
