package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// idCursor is a stable pagination position for id-ordered lists.
type idCursor struct {
	ID string `json:"i"`
}

// encodeIDCursor renders an opaque cursor for the item after id.
func encodeIDCursor(id string) string {
	if id == "" {
		return ""
	}
	raw := fmt.Sprintf(`{"i":%q}`, id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeIDCursor parses a cursor produced by encodeIDCursor.
func decodeIDCursor(cursor string) (idCursor, error) {
	if cursor == "" {
		return idCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return idCursor{}, err
	}
	var c idCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return idCursor{}, err
	}
	return c, nil
}

// timeIDCursor is a composite position for (created_ms DESC, id DESC)
// ordering — the shape virtual keys and request records use.
type timeIDCursor struct {
	CreatedMS int64  `json:"c"`
	ID        string `json:"i"`
}

// encodeTimeIDCursor renders an opaque composite cursor.
func encodeTimeIDCursor(createdMS int64, id string) string {
	if id == "" {
		return ""
	}
	raw := fmt.Sprintf(`{"c":%d,"i":%q}`, createdMS, id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeTimeIDCursor parses a cursor produced by encodeTimeIDCursor.
func decodeTimeIDCursor(cursor string) (timeIDCursor, error) {
	if cursor == "" {
		return timeIDCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return timeIDCursor{}, err
	}
	var c timeIDCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return timeIDCursor{}, err
	}
	return c, nil
}

// paginateByID pages an id-sorted slice with keyset semantics: the cursor
// names the last ID of the previous page; the next page starts strictly
// after it. Ordering must be ascending by ID (storage repositories sort
// by id). next is "" when the page is exhausted.
func paginateByID[T any](items []T, limit int, cursor string, idOf func(T) string) (page []T, next string, ge *domain.GatewayError) {
	after, err := decodeIDCursor(cursor)
	if err != nil {
		e := errInvalid("cursor is malformed", "cursor")
		return nil, "", &e
	}
	start := 0
	if after.ID != "" {
		for i, it := range items {
			if idOf(it) == after.ID {
				start = i + 1
				break
			}
		}
		// Cursor pointing past the end (or at a deleted row): the
		// keyset degrades to the empty page, never a wrong page.
		if start == 0 {
			return nil, "", nil
		}
	}
	end := start + limit
	if end >= len(items) {
		return items[start:], "", nil
	}
	page = items[start:end]
	return page, encodeIDCursor(idOf(page[len(page)-1])), nil
}

// paginateByTimeID pages a (created_ms DESC, id DESC) slice with keyset
// semantics. items must already be sorted that way.
func paginateByTimeID[T any](items []T, limit int, cursor string, keyOf func(T) (int64, string)) (page []T, next string, ge *domain.GatewayError) {
	after, err := decodeTimeIDCursor(cursor)
	if err != nil {
		e := errInvalid("cursor is malformed", "cursor")
		return nil, "", &e
	}
	start := 0
	if after.ID != "" || after.CreatedMS != 0 {
		for i, it := range items {
			ms, id := keyOf(it)
			if ms == after.CreatedMS && id == after.ID {
				start = i + 1
				break
			}
		}
		if start == 0 {
			return nil, "", nil
		}
	}
	end := start + limit
	if end >= len(items) {
		return items[start:], "", nil
	}
	page = items[start:end]
	ms, id := keyOf(page[len(page)-1])
	return page, encodeTimeIDCursor(ms, id), nil
}

// sortByID sorts ascending by ID (stable for equal IDs).
func sortByID[T any](items []T, idOf func(T) string) {
	sort.SliceStable(items, func(a, b int) bool {
		return idOf(items[a]) < idOf(items[b])
	})
}
