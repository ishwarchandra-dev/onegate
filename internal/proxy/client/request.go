package client

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/profile"
	"github.com/ishwarchandra-dev/onegate/internal/version"
)

// Provider is the resolved upstream target for one attempt: identity,
// wire protocol, base URL, and the API key resolved by the caller from
// encrypted storage (internal/auth) immediately before the call. The
// key transits only from the resolver into request headers here.
type Provider struct {
	ID       string
	Protocol domain.ProviderProtocol
	BaseURL  string

	// APIKey is the decrypted provider credential ("" = none configured).
	APIKey string

	// Profile selects an openai-compat flavor (internal/protocol/profile)
	// for Protocol == ProtocolOpenAIComp; ignored otherwise.
	Profile string

	// ExtraHeaders are merged into every request (e.g. OpenRouter
	// attribution). They are applied BEFORE auth, so credentials are
	// never overridable by configuration.
	ExtraHeaders map[string]string
}

// Request is one upstream attempt. Body is a byte slice on purpose:
// attempts are replayable (retry-safe transport) and no reader is ever
// half-consumed across fallback attempts.
type Request struct {
	// Path is the endpoint path with the leading slash, relative to
	// BaseURL, e.g. "/chat/completions" or
	// "/v1beta/models/gemini-2.0-flash:generateContent". Query goes in
	// RawQuery.
	Path     string
	RawQuery string
	Body     []byte

	// Method overrides the default POST. Empty means POST (every
	// proxy path is a POST); the management API's provider probe
	// (p6.api-impl) uses GET for protocol-native model-list checks.
	Method string

	// Stream switches Accept to text/event-stream.
	Stream bool
}

// upstreamUserAgent identifies the gateway to providers without leaking
// anything about the caller.
func upstreamUserAgent() string { return "onegate/" + version.Version }

// build constructs the request with per-protocol conventions:
//
//	openai         Bearer auth;        /chat/completions
//	anthropic      x-api-key + version /v1/messages
//	gemini         x-goog-api-key      /v1beta/models/{m}:generateContent
//	openai-compat  profile-driven auth/path (Groq, Mistral, vLLM, ...)
//
// Ordering contract: common headers, then provider extras, then auth.
// Credentials are applied last and cannot be clobbered by extras.
func (p Provider) build(r Request, guard *SSRFGuard) (*http.Request, error) {
	switch p.Protocol {
	case domain.ProtocolOpenAI, domain.ProtocolOpenAIComp:
		return p.buildOpenAI(r, guard)
	case domain.ProtocolAnthropic:
		return p.buildAnthropic(r, guard)
	case domain.ProtocolGemini:
		return p.buildGemini(r, guard)
	default:
		return nil, p.errf(domain.GatewayError{
			Status:  http.StatusInternalServerError,
			Type:    domain.ErrInternal,
			Message: fmt.Sprintf("unknown provider protocol %q", p.Protocol),
		}, nil)
	}
}

// requestMethod resolves the effective HTTP method (POST default).
func requestMethod(r Request) string {
	if r.Method != "" {
		return r.Method
	}
	return http.MethodPost
}

// buildOpenAI delegates URL and auth to the profile presets (shared
// with the openai adapter), then layers identity headers and extras on
// top and re-asserts auth last.
func (p Provider) buildOpenAI(r Request, guard *SSRFGuard) (*http.Request, error) {
	prof, err := profile.Resolve(openAIFlavor(p))
	if err != nil {
		return nil, p.errf(domain.GatewayError{
			Status:  http.StatusInternalServerError,
			Type:    domain.ErrInternal,
			Message: err.Error(),
		}, err)
	}
	if prof.APIKeyRequired && p.APIKey == "" {
		return nil, p.errf(domain.GatewayError{
			Status:  http.StatusBadGateway,
			Type:    domain.ErrAuthentication,
			Message: fmt.Sprintf("provider %s requires an API key; none configured", p.ID),
		}, nil)
	}
	hr, err := prof.Build(requestMethod(r), p.BaseURL, p.APIKey, r.Body)
	if err != nil {
		return nil, p.errf(domain.GatewayError{
			Status:  http.StatusBadGateway,
			Type:    domain.ErrInternal,
			Message: err.Error(),
		}, err)
	}
	// An explicit Request.Path is relative to BaseURL (the same contract
	// the anthropic/gemini builders honor); the profile preset pins the
	// chat path, so re-anchor the URL onto the base when a path is given
	// (the management probe uses this for /models style GETs).
	if r.Path != "" {
		u, err := url.Parse(strings.TrimRight(p.BaseURL, "/") + "/" + strings.TrimLeft(r.Path, "/"))
		if err != nil {
			return nil, p.errf(domain.GatewayError{
				Status:  http.StatusBadGateway,
				Type:    domain.ErrInternal,
				Message: err.Error(),
			}, err)
		}
		hr.URL = u
	}
	if r.RawQuery != "" {
		hr.URL.RawQuery = r.RawQuery
	}
	applyCommon(hr, r, p)
	applyProfileAuth(hr, prof, p.APIKey)
	return hr, guard.checkURL(hr.URL)
}

// buildAnthropic targets the Messages API. The version header is pinned
// by the adapter contract (docs/protocol-mappings.md section 2).
func (p Provider) buildAnthropic(r Request, guard *SSRFGuard) (*http.Request, error) {
	if p.APIKey == "" {
		return nil, p.errf(domain.GatewayError{
			Status:  http.StatusBadGateway,
			Type:    domain.ErrAuthentication,
			Message: fmt.Sprintf("provider %s requires an API key; none configured", p.ID),
		}, nil)
	}
	hr, err := p.newHTTPRequest(r)
	if err != nil {
		return nil, err
	}
	applyCommon(hr, r, p)
	hr.Header.Set("x-api-key", p.APIKey)
	hr.Header.Set("anthropic-version", anthropicVersion)
	return hr, guard.checkURL(hr.URL)
}

// buildGemini uses header auth (x-goog-api-key), never the ?key= query
// parameter: keys in URLs leak into access logs and error messages.
func (p Provider) buildGemini(r Request, guard *SSRFGuard) (*http.Request, error) {
	hr, err := p.newHTTPRequest(r)
	if err != nil {
		return nil, err
	}
	applyCommon(hr, r, p)
	if p.APIKey != "" {
		hr.Header.Set("x-goog-api-key", p.APIKey)
	}
	return hr, guard.checkURL(hr.URL)
}

// anthropicVersion pins the Messages API version the adapters speak.
const anthropicVersion = "2023-06-01"

// openAIFlavor maps protocol+profile to a profile id.
func openAIFlavor(p Provider) string {
	if p.Protocol == domain.ProtocolOpenAI || p.Profile == "" {
		return "openai"
	}
	return p.Profile
}

// newHTTPRequest builds the base POST for anthropic/gemini paths (the
// openai family goes through profile.Build).
func (p Provider) newHTTPRequest(r Request) (*http.Request, error) {
	url := strings.TrimRight(p.BaseURL, "/")
	if url == "" {
		return nil, p.errf(domain.GatewayError{
			Status:  http.StatusBadGateway,
			Type:    domain.ErrInternal,
			Message: fmt.Sprintf("provider %s has no base URL configured", p.ID),
		}, nil)
	}
	if r.Path != "" {
		url += "/" + strings.TrimLeft(r.Path, "/")
	}
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	hr, err := http.NewRequest(requestMethod(r), url, body)
	if err != nil {
		return nil, p.errf(domain.GatewayError{
			Status:  http.StatusBadGateway,
			Type:    domain.ErrInternal,
			Message: err.Error(),
		}, err)
	}
	if r.RawQuery != "" {
		hr.URL.RawQuery = r.RawQuery
	}
	return hr, nil
}

// applyCommon merges gateway identity and content headers, then
// provider extras. Auth is NOT set here — protocol builders apply it
// after this call so credentials always win.
func applyCommon(hr *http.Request, r Request, p Provider) {
	hr.Header.Set("User-Agent", upstreamUserAgent())
	hr.Header.Set("Content-Type", "application/json")
	if r.Stream {
		hr.Header.Set("Accept", "text/event-stream")
	} else {
		hr.Header.Set("Accept", "application/json")
	}
	for k, v := range p.ExtraHeaders {
		hr.Header.Set(k, v)
	}
}

// applyProfileAuth (re)asserts openai-family credentials after extras.
// Mirrors profile.Build's auth logic; profile.Build may already have
// set it, but re-asserting after extras keeps the invariant local.
func applyProfileAuth(hr *http.Request, prof profile.Profile, apiKey string) {
	if apiKey == "" {
		return
	}
	switch prof.Auth {
	case profile.AuthBearer:
		hr.Header.Set("Authorization", "Bearer "+apiKey)
	case profile.AuthHeader:
		if prof.AuthHeaderName != "" {
			hr.Header.Set(prof.AuthHeaderName, apiKey)
		}
	case profile.AuthQuery:
		q := hr.URL.Query()
		q.Set(prof.AuthQueryParam, apiKey)
		hr.URL.RawQuery = q.Encode()
	}
}

// errf wraps a GatewayError with provider identity and an optional
// cause into the package Error type.
func (p Provider) errf(ge domain.GatewayError, cause error) error {
	return &Error{Provider: p.ID, GErr: ge, Cause: cause}
}

// Error is the package's failure type. GErr is always set and is what
// the fallback engine and client-facing envelopes consume; Cause keeps
// the underlying transport error for logs.
type Error struct {
	Provider string
	GErr     domain.GatewayError
	Cause    error
}

func (e *Error) Error() string {
	if e.Provider != "" {
		return fmt.Sprintf("upstream %s: %s", e.Provider, e.GErr.Message)
	}
	return "upstream: " + e.GErr.Message
}

func (e *Error) Unwrap() error { return e.Cause }
