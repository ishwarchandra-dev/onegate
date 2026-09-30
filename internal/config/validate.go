package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ValidationError describes a single invalid field with its JSON path.
type ValidationError struct {
	Path    string // e.g. "port" or "reload.poll_ms"
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

// Validate checks the resolved config. It returns a *ValidationError
// pointing at the first offending field, so callers get precise errors
// rather than a generic "invalid config".
func Validate(cfg Config) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return &ValidationError{Path: "host", Message: "must not be empty"}
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return &ValidationError{
			Path:    "port",
			Message: fmt.Sprintf("must be in [1, 65535], got %d", cfg.Port),
		}
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return &ValidationError{
			Path:    "log_level",
			Message: fmt.Sprintf("must be one of debug|info|warn|error, got %q", cfg.LogLevel),
		}
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return &ValidationError{Path: "data_dir", Message: "must not be empty"}
	}
	if cfg.Reload.PollMS < 100 {
		return &ValidationError{
			Path:    "reload.poll_ms",
			Message: fmt.Sprintf("must be >= 100ms, got %d", cfg.Reload.PollMS),
		}
	}
	return nil
}

// jsonUnmarshalStrict parses JSON and enriches encoding/json errors with
// line/column information for syntax problems, and path information for
// type mismatches. Unknown fields are ignored (forward compatibility:
// newer config files on older binaries keep working).
func jsonUnmarshalStrict(path string, data []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(v); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			line, col := offsetToLineCol(data, int(syn.Offset))
			return fmt.Errorf("%s:%d:%d: invalid JSON: %s",
				path, line, col, syn.Error())
		}
		var unmarshal *json.UnmarshalTypeError
		if errors.As(err, &unmarshal) {
			return fmt.Errorf("%s: field %q: cannot use %s as %s",
				path, unmarshal.Field, unmarshal.Value, unmarshal.Type.String())
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// offsetToLineCol converts a byte offset into 1-based line/column.
func offsetToLineCol(data []byte, offset int) (int, int) {
	if offset > len(data) {
		offset = len(data)
	}
	line, col := 1, 1
	for i := 0; i < offset; i++ {
		if data[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

// parsePort parses a port from an environment string.
func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return p, nil
}
