package storage

import (
	"fmt"
	"strconv"
	"strings"
)

// cursor is the decoded pagination position: (created_ms, id) is unique
// and stable, giving keyset pagination with deterministic ordering.
type cursor struct {
	CreatedMS int64
	ID        string
}

func encodeCursor(createdMS int64, id string) string {
	return fmt.Sprintf("%d:%s", createdMS, id)
}

func decodeCursor(s string) (cursor, bool) {
	if s == "" {
		return cursor{}, false
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return cursor{}, false
	}
	ms, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return cursor{}, false
	}
	return cursor{CreatedMS: ms, ID: parts[1]}, true
}
