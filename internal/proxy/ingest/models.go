package ingest

// Model listing (checklist A-7, p7.parity-fixes): OmniRoute exposed
// GET /v1/models and clients (and the OpenAI SDKs' model picker) rely on
// it. The listing is key-scoped: a key with allowed_models sees only its
// models; unrestricted keys see everything.
import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// ModelInfo is one listing entry.
type ModelInfo struct {
	ID      string `json:"id"`
	Created int64  `json:"created"` // unix seconds
}

// ModelLister supplies the visible model catalog.
type ModelLister interface {
	// ListModels returns all known canonical models (sorted or not —
	// the handler sorts). Aliases are NOT listed separately; they
	// resolve, but the catalog shows canonical IDs only (OmniRoute
	// behavior, checklist A-7).
	ListModels() []ModelInfo
}

// handleModels serves GET /v1/models.
func (d Deps) handleModels(w http.ResponseWriter, r *http.Request) {
	raw := bearerToken(r.Header.Get("Authorization"))
	if raw == "" {
		raw = r.Header.Get("x-api-key")
	}
	if raw == "" {
		raw = r.Header.Get("x-goog-api-key")
	}
	if raw == "" {
		raw = r.URL.Query().Get("key")
	}
	key, ok := d.authenticate(w, r, domain.ProtocolOpenAI, raw)
	if !ok {
		return
	}

	models := d.Models.ListModels()
	allowed := key.Scopes.AllowedModels
	allowAll := len(allowed) == 0
	allowedSet := make(map[string]bool, len(allowed))
	for _, m := range allowed {
		allowedSet[m] = true
	}

	type entry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	data := make([]entry, 0, len(models))
	for _, m := range models {
		if !allowAll && !allowedSet[m.ID] {
			continue
		}
		data = append(data, entry{ID: m.ID, Object: "model", Created: m.Created, OwnedBy: "onegate"})
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		Object string  `json:"object"`
		Data   []entry `json:"data"`
	}{Object: "list", Data: data})
}
