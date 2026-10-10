package gemini

import (
	"encoding/json"
	"fmt"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// DecodeResponse parses a Gemini generateContent response into canonical
// form. Blocked responses (promptFeedback.blockReason, no candidates) map
// to a safety finish with the native reason preserved (mapping doc §3.2).
func DecodeResponse(body []byte) (domain.Response, error) {
	var w generateResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return domain.Response{}, fmt.Errorf("gemini: bad response body: %w", err)
	}

	resp := domain.Response{
		ID:   w.ResponseID,
		Role: domain.RoleAssistant,
		Provider: domain.ProviderMeta{
			ProviderModel: w.ModelVersion,
		},
	}
	resp.Model = w.ModelVersion

	// Blocked shape.
	if len(w.Candidates) == 0 {
		if w.PromptFeedback != nil && w.PromptFeedback.BlockReason != "" {
			resp.FinishReason = domain.FinishSafety
			resp.Provider.NativeFinishReason = w.PromptFeedback.BlockReason
			if w.UsageMetadata != nil {
				resp.Usage = DecodeUsage(w.UsageMetadata)
			}
			return resp, nil
		}
		return domain.Response{}, fmt.Errorf("gemini: response has no candidates")
	}

	cand := w.Candidates[0]
	resp.Provider.NativeFinishReason = cand.FinishReason

	sawCall := false
	if cand.Content != nil {
		for _, p := range cand.Content.Parts {
			blocks, err := decodePart(p)
			if err != nil {
				return domain.Response{}, err
			}
			for _, b := range blocks {
				if b.Type == domain.BlockToolCall {
					sawCall = true
				}
				resp.Content = append(resp.Content, b)
			}
		}
	}

	resp.FinishReason = DecodeFinishReason(cand.FinishReason, sawCall)
	if w.UsageMetadata != nil {
		resp.Usage = DecodeUsage(w.UsageMetadata)
	}
	return resp, nil
}

// EncodeResponse renders a canonical response as a Gemini body.
func EncodeResponse(resp domain.Response) ([]byte, error) {
	w := generateResponse{
		ResponseID:   resp.ID,
		ModelVersion: resp.Model,
	}

	// Blocked shape (safety finish, no content, native block reason).
	if len(resp.Content) == 0 && resp.FinishReason == domain.FinishSafety &&
		resp.Provider.NativeFinishReason != "" {
		w.PromptFeedback = &wirePromptFeedback{BlockReason: resp.Provider.NativeFinishReason}
		w.UsageMetadata = encodeUsagePtr(resp.Usage)
		return json.Marshal(w)
	}

	cand := wireCandidate{
		Content:      &wireContent{Role: "model"},
		FinishReason: EncodeFinishReason(resp.FinishReason),
	}
	sawCall := false
	for _, b := range resp.Content {
		parts, err := encodePart(b)
		if err != nil {
			return nil, err
		}
		if b.Type == domain.BlockToolCall {
			sawCall = true
		}
		cand.Content.Parts = append(cand.Content.Parts, parts...)
	}
	if sawCall {
		cand.FinishReason = EncodeFinishReason(domain.FinishToolCalls)
	}
	if len(cand.Content.Parts) == 0 {
		cand.Content = nil
	}
	w.Candidates = []wireCandidate{cand}
	w.UsageMetadata = encodeUsagePtr(resp.Usage)
	return json.Marshal(w)
}

// DecodeFinishReason maps a Gemini finishReason to canonical. STOP after a
// function call means tool_calls (Gemini has no dedicated reason)
// [quirk:gemini-stop-after-function-call].
func DecodeFinishReason(s string, sawCall bool) domain.FinishReason {
	switch s {
	case "STOP":
		if sawCall {
			return domain.FinishToolCalls
		}
		return domain.FinishStop
	case "MAX_TOKENS":
		return domain.FinishLength
	case "SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		return domain.FinishSafety
	case "RECITATION":
		return domain.FinishRecitation
	case "OTHER", "MALFORMED_FUNCTION_CALL", "":
		if sawCall {
			return domain.FinishToolCalls
		}
		return domain.FinishOther
	default:
		return domain.FinishOther
	}
}

// EncodeFinishReason maps a canonical finish reason to Gemini.
func EncodeFinishReason(r domain.FinishReason) string {
	switch r {
	case domain.FinishStop, domain.FinishToolCalls:
		return "STOP" // tool_calls is STOP + functionCall parts (mapping doc §3.2)
	case domain.FinishLength:
		return "MAX_TOKENS"
	case domain.FinishSafety, domain.FinishRefusal:
		return "SAFETY"
	case domain.FinishRecitation:
		return "RECITATION"
	case domain.FinishContentFilter:
		return "OTHER"
	default:
		return "OTHER"
	}
}

// DecodeUsage converts usageMetadata to canonical (thoughtsTokenCount is
// the reasoning-token carrier — mapping doc §3.2).
func DecodeUsage(w *wireUsageMetadata) domain.TokenUsage {
	return domain.TokenUsage{
		InputTokens:     w.PromptTokenCount,
		OutputTokens:    w.CandidatesTokenCount,
		TotalTokens:     w.TotalTokenCount,
		CacheReadTokens: w.CachedContentTokenCount,
		ReasoningTokens: w.ThoughtsTokenCount,
	}.WithTotalDerivation().Sanitize()
}

func encodeUsagePtr(u domain.TokenUsage) *wireUsageMetadata {
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 &&
		u.CacheReadTokens == 0 && u.ReasoningTokens == 0 {
		return nil
	}
	return &wireUsageMetadata{
		PromptTokenCount:        u.InputTokens,
		CandidatesTokenCount:    u.OutputTokens,
		TotalTokenCount:         u.TotalTokens,
		CachedContentTokenCount: u.CacheReadTokens,
		ThoughtsTokenCount:      u.ReasoningTokens,
	}
}
