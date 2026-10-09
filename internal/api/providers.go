package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// providerDTO is the wire shape of a provider: storage fields plus the
// display-only masked credential. domain.Provider already matches the
// contract; this alias documents the read model.
type providerDTO = domain.Provider

// providerCreate is the POST /api/providers body.
type providerCreate struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Protocol string  `json:"protocol"`
	BaseURL  string  `json:"base_url"`
	APIKey   *string `json:"api_key"`
	Enabled  *bool   `json:"enabled"`
}

// providerUpdate is the PATCH /api/providers/{id} body. Nil fields keep
// current values; a non-nil api_key rotates the credential.
type providerUpdate struct {
	Name    *string `json:"name"`
	BaseURL *string `json:"base_url"`
	APIKey  *string `json:"api_key"`
	Enabled *bool   `json:"enabled"`
}

// legalProtocols mirrors the spec enum (domain.ProviderProtocol values).
var legalProtocols = map[string]bool{
	"openai":        true,
	"anthropic":     true,
	"gemini":        true,
	"openai-compat": true,
}

// validateProviderShape checks the shared create/replace invariants.
func validateProviderShape(name, protocol, baseURL string) *domain.GatewayError {
	if strings.TrimSpace(name) == "" {
		e := errInvalid("name is required", "name")
		return &e
	}
	if !legalProtocols[protocol] {
		e := errInvalid("protocol must be one of openai, anthropic, gemini, openai-compat", "protocol")
		return &e
	}
	if err := validateBaseURL(baseURL); err != nil {
		return err
	}
	return nil
}

// validateBaseURL accepts only absolute http(s) URLs. The SSRF guard
// re-checks at call time; this is boundary validation for clear 400s.
func validateBaseURL(raw string) *domain.GatewayError {
	if raw == "" {
		e := errInvalid("base_url is required", "base_url")
		return &e
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		e := errInvalid("base_url must be an absolute URL", "base_url")
		return &e
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		e := errInvalid("base_url scheme must be http or https", "base_url")
		return &e
	}
	return nil
}

// toProviderDTO renders the storage record as the wire shape, masking the
// stored credential. Decryption failures surface as an absent key, never
// a 500 — a rotated master secret should not take down the provider list.
func (a *API) toProviderDTO(rec storage.ProviderRecord) providerDTO {
	p := rec.Provider
	p.MaskedKey = ""
	if len(rec.APIKeyEnc) > 0 && a.opts.ProviderCipher != nil {
		if plain, err := a.opts.ProviderCipher.Decrypt(rec.APIKeyEnc); err == nil && len(plain) > 0 {
			p.MaskedKey = auth.MaskKey(string(plain))
		}
	}
	return p
}

// handleListProviders GET /api/providers
func (a *API) handleListProviders(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	recs, err := a.opts.Store.Providers().List()
	if err != nil {
		a.mapStorageError(w, err, "providers")
		return
	}
	a.renderProviderPage(w, recs, limit, r.URL.Query().Get("cursor"))
}

// renderProviderPage paginates and renders provider records.
func (a *API) renderProviderPage(w http.ResponseWriter, recs []storage.ProviderRecord, limit int, cursor string) {
	page, next, ge := paginateByID(recs, limit, cursor, func(rec storage.ProviderRecord) string { return rec.ID })
	if ge != nil {
		writeError(w, *ge)
		return
	}
	items := make([]providerDTO, 0, len(page))
	for _, rec := range page {
		items = append(items, a.toProviderDTO(rec))
	}
	writePage(w, items, next)
}

// handleCreateProvider POST /api/providers
func (a *API) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	var body providerCreate
	if !decodeJSON(w, r, &body) {
		return
	}
	if verr := validateProviderShape(body.Name, body.Protocol, body.BaseURL); verr != nil {
		writeError(w, *verr)
		return
	}

	// Idempotent create: a known id returns the existing provider (200).
	if body.ID != "" {
		if existing, err := a.opts.Store.Providers().Get(body.ID); err == nil {
			writeJSON(w, http.StatusOK, a.toProviderDTO(existing))
			return
		} else if !errors.Is(err, storage.ErrNotFound) {
			a.mapStorageError(w, err, "provider")
			return
		}
	}

	rec := storage.ProviderRecord{
		Provider: domain.Provider{
			ID:       body.ID,
			Name:     strings.TrimSpace(body.Name),
			Protocol: domain.ProviderProtocol(body.Protocol),
			BaseURL:  strings.TrimRight(body.BaseURL, "/"),
			Enabled:  body.Enabled == nil || *body.Enabled,
		},
	}
	if err := a.sealProviderKey(&rec, body.APIKey); err != nil {
		a.mapStorageError(w, err, "provider")
		return
	}
	if err := a.opts.Store.Providers().Upsert(&rec); err != nil {
		a.mapStorageError(w, err, "provider")
		return
	}
	writeJSON(w, http.StatusCreated, a.toProviderDTO(rec))
}

// sealProviderKey encrypts the credential (if any) into the record.
func (a *API) sealProviderKey(rec *storage.ProviderRecord, apiKey *string) error {
	if apiKey == nil || *apiKey == "" {
		rec.APIKeyEnc = []byte{}
		return nil
	}
	if a.opts.ProviderCipher == nil {
		return errors.New("api: provider cipher not configured")
	}
	enc, err := a.opts.ProviderCipher.Encrypt([]byte(*apiKey))
	if err != nil {
		return err
	}
	rec.APIKeyEnc = enc
	return nil
}

// handleGetProvider GET /api/providers/{id}
func (a *API) handleGetProvider(w http.ResponseWriter, r *http.Request) {
	rec, err := a.opts.Store.Providers().Get(pathID(r))
	if err != nil {
		a.mapStorageError(w, err, "provider")
		return
	}
	writeJSON(w, http.StatusOK, a.toProviderDTO(rec))
}

// handleUpdateProvider PATCH /api/providers/{id}
func (a *API) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	var body providerUpdate
	if !decodeJSON(w, r, &body) {
		return
	}
	id := pathID(r)
	rec, err := a.opts.Store.Providers().Get(id)
	if err != nil {
		a.mapStorageError(w, err, "provider")
		return
	}
	if body.Name != nil {
		if strings.TrimSpace(*body.Name) == "" {
			writeError(w, errInvalid("name cannot be empty", "name"))
			return
		}
		rec.Name = strings.TrimSpace(*body.Name)
	}
	if body.BaseURL != nil {
		if verr := validateBaseURL(*body.BaseURL); verr != nil {
			writeError(w, *verr)
			return
		}
		rec.BaseURL = strings.TrimRight(*body.BaseURL, "/")
	}
	if body.Enabled != nil {
		rec.Enabled = *body.Enabled
	}
	if body.APIKey != nil {
		if err := a.sealProviderKey(&rec, body.APIKey); err != nil {
			a.mapStorageError(w, err, "provider")
			return
		}
	}
	if err := a.opts.Store.Providers().Upsert(&rec); err != nil {
		a.mapStorageError(w, err, "provider")
		return
	}
	writeJSON(w, http.StatusOK, a.toProviderDTO(rec))
}

// handleDeleteProvider DELETE /api/providers/{id}
func (a *API) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := a.opts.Store.Providers().Delete(pathID(r)); err != nil {
		a.mapStorageError(w, err, "provider")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// providerTestResult is the probe response.
type providerTestResult struct {
	OK        bool                 `json:"ok"`
	LatencyMS int64                `json:"latency_ms"`
	Error     *errorEnvelopeInline `json:"error,omitempty"`
}

// errorEnvelopeInline mirrors the wire error object inside the probe
// result (a nested {"error": {...}} per the spec's ProviderTestResult).
type errorEnvelopeInline = struct {
	Error domain.GatewayError `json:"error"`
}

// probeTimeout bounds the outbound test call.
const probeTimeout = 10 * time.Second

// handleTestProvider POST /api/providers/{id}/test
//
// Performs a protocol-native model-list GET through the same
// SSRF-guarded transport the proxy uses — a management probe must never
// bypass the guard.
func (a *API) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	if a.opts.Prober == nil {
		writeError(w, domain.GatewayError{
			Status:  http.StatusServiceUnavailable,
			Type:    domain.ErrAPI,
			Code:    "probe_unavailable",
			Message: "probe transport not configured",
		})
		return
	}
	rec, err := a.opts.Store.Providers().Get(pathID(r))
	if err != nil {
		a.mapStorageError(w, err, "provider")
		return
	}

	prov := client.Provider{
		ID:       rec.ID,
		Protocol: rec.Protocol,
		BaseURL:  rec.BaseURL,
	}
	if len(rec.APIKeyEnc) > 0 && a.opts.ProviderCipher != nil {
		if plain, derr := a.opts.ProviderCipher.Decrypt(rec.APIKeyEnc); derr == nil {
			prov.APIKey = string(plain)
		}
	}

	path, rerr := probePath(rec.Protocol)
	if rerr != nil {
		writeError(w, errInvalid(rerr.Error(), "protocol"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	start := a.opts.NowMS()
	resp, derr := a.opts.Prober.Do(ctx, prov, client.Request{Method: http.MethodGet, Path: path})
	latency := a.opts.NowMS() - start
	if derr != nil {
		writeJSON(w, http.StatusOK, providerTestResult{
			OK:        false,
			LatencyMS: latency,
			Error:     &errorEnvelopeInline{Error: probeClientError(derr)},
		})
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		writeJSON(w, http.StatusOK, providerTestResult{OK: true, LatencyMS: latency})
		return
	}
	ge := probeHTTPError(rec.Protocol, resp.StatusCode)
	writeJSON(w, http.StatusOK, providerTestResult{
		OK:        false,
		LatencyMS: latency,
		Error:     &errorEnvelopeInline{Error: ge},
	})
}

// probePath selects the protocol-native model-list endpoint.
func probePath(p domain.ProviderProtocol) (string, error) {
	switch p {
	case domain.ProtocolOpenAI, domain.ProtocolOpenAIComp:
		return "/models", nil
	case domain.ProtocolAnthropic:
		return "/v1/models", nil
	case domain.ProtocolGemini:
		return "/v1beta/models", nil
	default:
		return "", errors.New("unknown provider protocol")
	}
}

// probeClientError maps transport failures onto the envelope.
func probeClientError(err error) domain.GatewayError {
	var cerr *client.Error
	if errors.As(err, &cerr) {
		return cerr.GErr
	}
	return domain.GatewayError{
		Status:  http.StatusBadGateway,
		Type:    domain.ErrAPI,
		Code:    "probe_failed",
		Message: "provider unreachable: " + err.Error(),
	}
}

// probeHTTPError maps a non-2xx provider response onto the envelope,
// preserving the provider's own status so the dashboard can render
// exactly what the provider reported.
func probeHTTPError(_ domain.ProviderProtocol, status int) domain.GatewayError {
	var t domain.ErrorType
	switch {
	case status == 401 || status == 403:
		t = domain.ErrAuthentication
	case status == 429:
		t = domain.ErrRateLimit
	case status >= 500:
		t = domain.ErrAPI
	default:
		t = domain.ErrAPI
	}
	return domain.GatewayError{
		Status:  status,
		Type:    t,
		Code:    "provider_returned_error",
		Message: "provider responded with a non-success status",
	}
}
