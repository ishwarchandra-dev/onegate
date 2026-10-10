package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// DecodeResponse parses an Anthropic messages response into canonical form.
func DecodeResponse(body []byte) (domain.Response, error) {
	var w messagesResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return domain.Response{}, fmt.Errorf("anthropic: bad response body: %w", err)
	}

	resp := domain.Response{
		ID:   w.ID,
		Role: domain.RoleAssistant,
		Provider: domain.ProviderMeta{
			ProviderModel:      w.Model,
			NativeFinishReason: derefString(w.StopReason),
		},
	}
	resp.Model = w.Model

	for _, b := range w.Content {
		cb, err := decodeBlock(b)
		if err != nil {
			return domain.Response{}, err
		}
		resp.Content = append(resp.Content, cb)
	}

	resp.FinishReason = DecodeFinishReason(derefString(w.StopReason))
	resp.Usage = DecodeUsage(w.Usage)
	return resp, nil
}

// EncodeResponse renders a canonical response as an Anthropic body.
func EncodeResponse(resp domain.Response) ([]byte, error) {
	w := messagesResponse{
		ID:         resp.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      resp.Model,
		StopReason: stringPtr(EncodeFinishReason(resp.FinishReason)),
		Usage:      encodeUsage(resp.Usage),
	}
	for _, b := range resp.Content {
		wb, err := encodeBlock(b)
		if err != nil {
			return nil, err
		}
		w.Content = append(w.Content, wb)
	}
	if w.Content == nil {
		w.Content = []wireBlock{}
	}
	return json.Marshal(w)
}

// DecodeFinishReason maps an Anthropic stop_reason to canonical.
func DecodeFinishReason(s string) domain.FinishReason {
	switch s {
	case "end_turn", "stop_sequence", "pause_turn":
		return domain.FinishStop
	case "max_tokens":
		return domain.FinishLength
	case "tool_use":
		return domain.FinishToolCalls
	case "refusal":
		return domain.FinishRefusal
	default:
		return domain.FinishStop
	}
}

// EncodeFinishReason maps a canonical finish reason to Anthropic. safety/
// recitation/content_filter fold to refusal (native reason preserved in
// provider meta — mapping doc §2.2).
func EncodeFinishReason(r domain.FinishReason) string {
	switch r {
	case domain.FinishStop:
		return "end_turn"
	case domain.FinishLength:
		return "max_tokens"
	case domain.FinishToolCalls:
		return "tool_use"
	case domain.FinishRefusal, domain.FinishSafety, domain.FinishRecitation, domain.FinishContentFilter:
		return "refusal"
	default:
		return "end_turn"
	}
}

// DecodeUsage converts wire usage to canonical (total derived; 5m/1h cache
// TTL split collapsed — ADR 004) [quirk:anthropic-cache-ttl-split].
func DecodeUsage(w wireUsage) domain.TokenUsage {
	return domain.TokenUsage{
		InputTokens:      w.InputTokens,
		OutputTokens:     w.OutputTokens,
		CacheWriteTokens: w.CacheCreationInputTokens,
		CacheReadTokens:  w.CacheReadInputTokens,
	}.WithTotalDerivation().Sanitize()
}

func encodeUsage(u domain.TokenUsage) wireUsage {
	return wireUsage{
		InputTokens:              u.InputTokens,
		CacheCreationInputTokens: u.CacheWriteTokens,
		CacheReadInputTokens:     u.CacheReadTokens,
		OutputTokens:             u.OutputTokens,
	}
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func stringPtr(s string) *string { return &s }
