// Package ingest implements OneGate's client-facing proxy endpoints:
// credential extraction, key verification, protocol detection, body
// validation, and handoff to the proxy engine.
//
// Routing table (OmniRoute v3.8.52 parity — the three protocol
// families the gateway speaks natively):
//
//	POST /v1/chat/completions                     OpenAI
//	POST /v1/messages                             Anthropic
//	POST /v1beta/models/{model}:generateContent   Gemini
//	POST /v1beta/models/{model}:streamGenerateContent  Gemini (SSE)
//
// Additional compatibility surfaces (e.g. /v1/models listings) are
// audited and added in Phase 7 (p7.parity-checklist).
//
// Error contract: every client-facing failure renders in the CALLING
// protocol's envelope ({"error":{...}} OpenAI, {"type":"error",...}
// Anthropic, {"error":{code,message,status}} Gemini) via the adapters'
// EncodeError — canonical-at-the-edge applies to errors too. Status
// semantics: 401 unknown/missing credential, 403 revoked/expired key,
// 400 malformed body, 413 oversize body.
//
// The package depends on interfaces, not implementations: Authenticator
// (backed by internal/auth + storage in production) and Proxy (the
// fallback engine, p3.fallback-chain). Handlers never import storage.
package ingest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// defaultMaxBodyBytes caps request bodies. Generous on purpose: vision
// payloads embed base64 images (a few MB per image).
const defaultMaxBodyBytes = 32 << 20 // 32 MiB

// Authenticator verifies raw client credentials.
// Production implementation: auth.Verifier (storage-backed).
type Authenticator interface {
	Verify(ctx context.Context, rawKey string) (domain.VirtualKey, error)
}

// Proxy executes a canonical call and writes the client response
// (buffered or streaming). Implemented by the fallback engine
// (p3.fallback-chain / p3.nonstream-path / p3.stream-pipeline).
type Proxy interface {
	Execute(ctx context.Context, w http.ResponseWriter, call Call)
}

// Call is the fully-resolved inbound request handed to Proxy.
type Call struct {
	// Protocol is the client-facing wire protocol (from the endpoint).
	Protocol domain.ProviderProtocol

	// Request is the canonical decode of the client body.
	Request domain.Request

	// Body is the raw client JSON (diagnostics + passthrough decisions).
	Body []byte

	// Stream requests an SSE response.
	Stream bool

	// Key is the authenticated virtual key (scopes, limits).
	Key domain.VirtualKey
}

// Deps wires the endpoints. Auth and Proxy are required.
type Deps struct {
	Auth   Authenticator
	Proxy  Proxy
	Logger *slog.Logger

	// MaxBodyBytes caps request bodies (default 32 MiB).
	MaxBodyBytes int64
}

// Register mounts the proxy endpoints on mux. Callers pass the server
// router's mux (server.Router.Mux()); this package stays independent of
// the server package by design.
func Register(mux *http.ServeMux, deps Deps) {
	if deps.Auth == nil {
		panic("ingest: Register requires an Authenticator")
	}
	if deps.Proxy == nil {
		panic("ingest: Register requires a Proxy")
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.MaxBodyBytes <= 0 {
		deps.MaxBodyBytes = defaultMaxBodyBytes
	}

	mux.HandleFunc("POST /v1/chat/completions", deps.handleOpenAI)
	mux.HandleFunc("POST /v1/messages", deps.handleAnthropic)
	// ServeMux wildcards match whole path segments, and the Gemini method
	// suffix (":generateContent") shares a segment with the model, so the
	// tail is matched broadly and parsed in the handler.
	mux.HandleFunc("POST /v1beta/models/{target...}", deps.handleGemini)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (d Deps) handleOpenAI(w http.ResponseWriter, r *http.Request) {
	raw := bearerToken(r.Header.Get("Authorization"))
	if raw == "" {
		// OmniRoute tolerated x-api-key from OpenAI-style clients.
		raw = r.Header.Get("x-api-key")
	}
	d.serve(w, r, domain.ProtocolOpenAI, raw, openaiDecode)
}

func (d Deps) handleAnthropic(w http.ResponseWriter, r *http.Request) {
	raw := r.Header.Get("x-api-key")
	if raw == "" {
		raw = bearerToken(r.Header.Get("Authorization"))
	}
	d.serve(w, r, domain.ProtocolAnthropic, raw, anthropicDecode)
}

func (d Deps) handleGemini(w http.ResponseWriter, r *http.Request) {
	// Official SDKs send x-goog-api-key; ?key= is the documented
	// alternative — both accepted.
	raw := r.Header.Get("x-goog-api-key")
	if raw == "" {
		raw = r.URL.Query().Get("key")
	}

	target := r.PathValue("target") // "{model}:{method}"
	model, method, ok := splitGeminiTarget(target)
	if !ok {
		renderError(w, domain.ProtocolGemini, domain.GatewayError{
			Status:  http.StatusNotFound,
			Type:    domain.ErrNotFound,
			Message: "unknown Gemini endpoint; expected models/{model}:generateContent or :streamGenerateContent",
		})
		return
	}
	stream := method == "streamGenerateContent"

	d.serve(w, r, domain.ProtocolGemini, raw, func(body []byte) (domain.Request, error) {
		req, err := geminiDecode(body)
		if err != nil {
			return req, err
		}
		// The Gemini stream flag lives in the path, not the body; the
		// model lives in the path, not the body.
		req.Stream = stream
		if req.Model == "" {
			req.Model = model
		}
		return req, nil
	})
}

// serve is the shared ingest pipeline: authenticate -> read (capped) ->
// decode -> hand off to the proxy engine.
func (d Deps) serve(w http.ResponseWriter, r *http.Request, protocol domain.ProviderProtocol, rawKey string, decode func([]byte) (domain.Request, error)) {
	key, ok := d.authenticate(w, r, protocol, rawKey)
	if !ok {
		return
	}

	body, ok := d.readBody(w, protocol, r)
	if !ok {
		return
	}

	req, err := decode(body)
	if err != nil {
		renderError(w, protocol, domain.GatewayError{
			Status:  http.StatusBadRequest,
			Type:    domain.ErrInvalidRequest,
			Message: err.Error(),
		})
		return
	}

	d.Proxy.Execute(r.Context(), w, Call{
		Protocol: protocol,
		Request:  req,
		Body:     body,
		Stream:   req.Stream,
		Key:      key,
	})
}

// authenticate runs credential verification and renders 401/403 in the
// calling protocol's envelope on failure.
func (d Deps) authenticate(w http.ResponseWriter, r *http.Request, protocol domain.ProviderProtocol, rawKey string) (domain.VirtualKey, bool) {
	key, err := d.Auth.Verify(r.Context(), rawKey)
	if err == nil {
		return key, true
	}
	ge := domain.GatewayError{
		Status: http.StatusUnauthorized,
		Type:   domain.ErrAuthentication,
	}
	switch {
	case err == domain.ErrNoCredential:
		ge.Code = "missing_api_key"
		ge.Message = "missing API key"
	case err == domain.ErrMalformedKey:
		ge.Code = "invalid_api_key"
		ge.Message = "invalid API key"
	case err == domain.ErrUnknownKey:
		ge.Code = "invalid_api_key"
		ge.Message = "invalid API key"
	case err == domain.ErrKeyRevoked:
		ge.Status = http.StatusForbidden
		ge.Type = domain.ErrPermission
		ge.Code = "key_revoked"
		ge.Message = "API key has been revoked"
	case err == domain.ErrKeyExpired:
		ge.Status = http.StatusForbidden
		ge.Type = domain.ErrPermission
		ge.Code = "key_expired"
		ge.Message = "API key has expired"
	default:
		ge.Type = domain.ErrAPI
		ge.Message = "authentication unavailable"
	}
	renderError(w, protocol, ge)
	return domain.VirtualKey{}, false
}

// readBody reads the request body under the configured cap. Oversize
// bodies fail fast with the protocol's 413 envelope.
func (d Deps) readBody(w http.ResponseWriter, protocol domain.ProviderProtocol, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, d.MaxBodyBytes+1))
	if err != nil {
		renderError(w, protocol, domain.GatewayError{
			Status:  http.StatusBadRequest,
			Type:    domain.ErrInvalidRequest,
			Message: "failed reading request body",
		})
		return nil, false
	}
	if int64(len(body)) > d.MaxBodyBytes {
		renderError(w, protocol, domain.GatewayError{
			Status:  http.StatusRequestEntityTooLarge,
			Type:    domain.ErrTooLarge,
			Code:    "request_body_too_large",
			Message: "request body exceeds the configured limit",
		})
		return nil, false
	}
	return body, true
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// bearerToken extracts the token from an Authorization header,
// case-insensitive on the scheme per RFC 7235.
func bearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// splitGeminiTarget splits "model:method" at the LAST colon (model ids
// may contain colons in some provider variants; methods never do).
func splitGeminiTarget(target string) (model, method string, ok bool) {
	i := strings.LastIndex(target, ":")
	if i <= 0 || i == len(target)-1 {
		return "", "", false
	}
	model, method = target[:i], target[i+1:]
	switch method {
	case "generateContent", "streamGenerateContent":
		return model, method, true
	default:
		return "", "", false
	}
}
