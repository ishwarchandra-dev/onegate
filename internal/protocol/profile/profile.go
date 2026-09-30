// Package profile defines transport profiles for OpenAI-compatible
// providers (OpenRouter, Groq, Mistral, vLLM, Ollama, plain OpenAI): the
// endpoint path, the auth scheme (with {key} header templating), and any
// provider-mandated extra headers. Bodies are produced by the openai
// adapter; profiles shape only the HTTP envelope.
//
// Quirk citations live in docs/research/provider-quirks.md (entries
// profile-*).
package profile

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
)

// AuthStyle selects how the API key is attached to upstream requests.
type AuthStyle string

const (
	// AuthBearer sets "Authorization: Bearer <key>".
	AuthBearer AuthStyle = "bearer"
	// AuthHeader sets a named header to the raw key.
	AuthHeader AuthStyle = "header"
	// AuthQuery appends the key as a query parameter.
	AuthQuery AuthStyle = "query"
	// AuthNone sends no credentials (Ollama default).
	AuthNone AuthStyle = "none"
)

// Profile is a provider transport shape.
type Profile struct {
	ID string

	// Path is the chat-completions endpoint path appended to the provider
	// base URL. Default "/chat/completions".
	Path string

	// Auth is the credential style.
	Auth AuthStyle
	// AuthHeaderName is the header name for AuthHeader (e.g. "api-key").
	AuthHeaderName string
	// AuthQueryParam is the query parameter for AuthQuery.
	AuthQueryParam string

	// APIKeyRequired rejects empty keys at build time.
	APIKeyRequired bool

	// ExtraHeaders are appended to every request. Values may contain the
	// "{key}" placeholder, which is replaced with the API key
	// (auth templating — e.g. gateways that want the key in a custom
	// header).
	ExtraHeaders map[string]string
}

// keyPlaceholder is the templating token in ExtraHeaders values.
const keyPlaceholder = "{key}"

var presets = map[string]Profile{
	// Canonical OpenAI API.
	"openai": {
		ID: "openai", Path: "/chat/completions", Auth: AuthBearer, APIKeyRequired: true,
	},
	// OpenRouter: Bearer auth; HTTP-Referer and X-Title are strongly
	// recommended for app attribution [quirk:profile-openrouter-headers].
	"openrouter": {
		ID: "openrouter", Path: "/chat/completions", Auth: AuthBearer, APIKeyRequired: true,
	},
	// Groq: OpenAI-compatible surface under /openai/v1
	// [quirk:profile-groq-baseurl].
	"groq": {
		ID: "groq", Path: "/chat/completions", Auth: AuthBearer, APIKeyRequired: true,
	},
	// Mistral: /v1/chat/completions.
	"mistral": {
		ID: "mistral", Path: "/chat/completions", Auth: AuthBearer, APIKeyRequired: true,
	},
	// vLLM: self-hosted; the key is optional server-side.
	"vllm": {
		ID: "vllm", Path: "/chat/completions", Auth: AuthBearer, APIKeyRequired: false,
	},
	// Ollama: local; no credentials by default [quirk:profile-ollama-auth].
	"ollama": {
		ID: "ollama", Path: "/chat/completions", Auth: AuthNone, APIKeyRequired: false,
	},
}

// DefaultPath is used when a profile carries no explicit path.
const DefaultPath = "/chat/completions"

// Resolve returns the profile for a provider flavor. Unknown or empty IDs
// resolve to the plain OpenAI shape; known IDs return their preset with
// defaults applied.
func Resolve(id string) (Profile, error) {
	if id == "" {
		id = "openai"
	}
	p, ok := presets[id]
	if !ok {
		return Profile{}, fmt.Errorf("profile: unknown provider profile %q", id)
	}
	if p.Path == "" {
		p.Path = DefaultPath
	}
	return p, nil
}

// IDs lists the built-in profile ids (sorted).
func IDs() []string {
	return []string{"groq", "mistral", "ollama", "openai", "openrouter", "vllm"}
}

// Build constructs the upstream HTTP request for a chat-completions call.
// baseURL is the provider base (scheme + host + version prefix, e.g.
// "https://api.groq.com/openai/v1"); body is the already-encoded
// OpenAI-format payload.
func (p Profile) Build(method, baseURL, apiKey string, body []byte) (*http.Request, error) {
	if p.Path == "" {
		p.Path = DefaultPath
	}
	if p.APIKeyRequired && apiKey == "" {
		return nil, fmt.Errorf("profile %s: API key required but not configured", p.ID)
	}

	url := strings.TrimRight(baseURL, "/") + p.Path
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("profile %s: bad request: %w", p.ID, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	switch p.Auth {
	case AuthBearer:
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	case AuthHeader:
		if p.AuthHeaderName == "" {
			return nil, fmt.Errorf("profile %s: header auth without a header name", p.ID)
		}
		if apiKey != "" {
			req.Header.Set(p.AuthHeaderName, apiKey)
		}
	case AuthQuery:
		if p.AuthQueryParam == "" {
			return nil, fmt.Errorf("profile %s: query auth without a param name", p.ID)
		}
		if apiKey != "" {
			q := req.URL.Query()
			q.Set(p.AuthQueryParam, apiKey)
			req.URL.RawQuery = q.Encode()
		}
	case AuthNone:
		// no credentials
	}

	for k, v := range p.ExtraHeaders {
		req.Header.Set(k, strings.ReplaceAll(v, keyPlaceholder, apiKey))
	}
	return req, nil
}
