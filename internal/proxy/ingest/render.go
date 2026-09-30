package ingest

import (
	"net/http"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/anthropic"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/gemini"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/openai"
)

// renderError writes a canonical error in the calling protocol's
// envelope — the same encoding the adapters use for provider errors,
// so clients see one consistent error schema per protocol whether the
// failure came from the gateway or the provider.
func renderError(w http.ResponseWriter, protocol domain.ProviderProtocol, ge domain.GatewayError) {
	var body []byte
	var status int
	switch protocol {
	case domain.ProtocolAnthropic:
		body, status = anthropic.EncodeError(ge)
	case domain.ProtocolGemini:
		body, status = gemini.EncodeError(ge)
	default: // openai + openai-compat clients
		body, status = openai.EncodeError(ge)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Decode wrappers keep the handler code protocol-agnostic while the
// adapter choice stays in one dispatch point.

func openaiDecode(body []byte) (domain.Request, error) {
	return openai.DecodeRequest(body)
}

func anthropicDecode(body []byte) (domain.Request, error) {
	return anthropic.DecodeRequest(body)
}

func geminiDecode(body []byte) (domain.Request, error) {
	return gemini.DecodeRequest(body)
}
