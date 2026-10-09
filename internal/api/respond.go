package api

import (
	"encoding/json"
	"net/http"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// writeJSON renders a JSON body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError renders the canonical {"error": {...}} envelope from a
// domain.GatewayError — the same shape the proxy data plane uses, so the
// dashboard has exactly one error format to parse.
func writeError(w http.ResponseWriter, ge domain.GatewayError) {
	writeJSON(w, ge.Status, struct {
		Error domain.GatewayError `json:"error"`
	}{Error: ge})
}

// writePage renders a cursor-paginated list response. items may be nil
// (renders as an empty JSON array via the explicit slice below).
func writePage(w http.ResponseWriter, items any, nextCursor string) {
	if items == nil {
		items = []any{}
	}
	writeJSON(w, http.StatusOK, pageMeta{Items: items, NextCursor: nextCursor})
}
