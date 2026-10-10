package openai

import (
	"encoding/json"
	"fmt"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// DecodeResponse parses an OpenAI chat-completions response into canonical
// form. Only choices[0] is honored (n>1 is an ADR 004 exclusion).
func DecodeResponse(body []byte) (domain.Response, error) {
	var w chatResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return domain.Response{}, fmt.Errorf("openai: bad response body: %w", err)
	}
	if len(w.Choices) == 0 {
		return domain.Response{}, fmt.Errorf("openai: response has no choices")
	}

	ch := w.Choices[0]
	resp := domain.Response{
		ID:        w.ID,
		Model:     w.Model,
		Role:      domain.RoleAssistant,
		CreatedMS: w.Created * 1000,
		Provider:  domain.ProviderMeta{ProviderModel: w.Model, NativeFinishReason: ch.FinishReason},
	}

	if ch.Message.ReasoningContent != "" {
		resp.Content = append(resp.Content, domain.ContentBlock{Type: domain.BlockThinking, Text: ch.Message.ReasoningContent})
	}
	if ch.Message.Content != nil && *ch.Message.Content != "" {
		resp.Content = append(resp.Content, domain.ContentBlock{Type: domain.BlockText, Text: *ch.Message.Content})
	}
	for _, tc := range ch.Message.ToolCalls {
		args := "{}"
		name := ""
		if tc.Function != nil {
			if tc.Function.Arguments != "" {
				args = tc.Function.Arguments
			}
			name = tc.Function.Name
		}
		resp.Content = append(resp.Content, domain.ContentBlock{
			Type: domain.BlockToolCall,
			Call: &domain.ToolCall{ID: tc.ID, Name: name, Arguments: args},
		})
	}
	if ch.Message.Refusal != nil && *ch.Message.Refusal != "" {
		resp.Content = append(resp.Content, domain.ContentBlock{Type: domain.BlockText, Text: *ch.Message.Refusal})
	}

	resp.FinishReason = DecodeFinishReason(ch.FinishReason)
	if ch.Message.Refusal != nil && *ch.Message.Refusal != "" && resp.FinishReason == domain.FinishStop {
		resp.FinishReason = domain.FinishRefusal
	}

	if w.Usage != nil {
		resp.Usage = DecodeUsage(w.Usage)
	} else {
		resp.Usage = resp.Usage.WithTotalDerivation()
	}
	return resp, nil
}

// EncodeResponse renders a canonical response as an OpenAI body.
func EncodeResponse(resp domain.Response) ([]byte, error) {
	w := chatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: resp.CreatedMS / 1000,
		Model:   resp.Model,
	}

	msg := wireRespMessage{Role: "assistant"}

	var textParts []string
	var thinking string
	var calls []wireToolCall
	for _, b := range resp.Content {
		switch b.Type {
		case domain.BlockText:
			textParts = append(textParts, b.Text)
		case domain.BlockThinking:
			thinking += b.Text
		case domain.BlockToolCall:
			calls = append(calls, wireToolCall{
				ID:   b.Call.ID,
				Type: "function",
				Function: &wireFnArg{
					Name:      b.Call.Name,
					Arguments: b.Call.Arguments,
				},
			})
		}
	}
	if len(textParts) > 0 {
		s := joinStrings(textParts, "\n\n")
		msg.Content = &s
	}
	msg.ReasoningContent = thinking
	msg.ToolCalls = calls
	// Content is always emitted (null when only tool calls) — OpenAI shape.
	if msg.Content == nil {
		s := ""
		msg.Content = &s
		if len(calls) > 0 {
			msg.Content = nil // explicit null
		}
	}

	w.Choices = []wireChoice{{
		Index:        0,
		Message:      msg,
		FinishReason: EncodeFinishReason(resp.FinishReason),
	}}
	w.Usage = encodeUsagePtr(resp.Usage)

	return json.Marshal(w)
}

// DecodeFinishReason maps an OpenAI finish_reason to canonical.
func DecodeFinishReason(s string) domain.FinishReason {
	switch s {
	case "stop":
		return domain.FinishStop
	case "length":
		return domain.FinishLength
	case "tool_calls", "function_call":
		return domain.FinishToolCalls
	case "content_filter":
		return domain.FinishContentFilter
	default:
		return domain.FinishStop
	}
}

// EncodeFinishReason maps a canonical finish reason to OpenAI. safety and
// recitation fold to content_filter; refusal folds to stop (native reason
// preserved in provider meta — mapping doc §1.2).
func EncodeFinishReason(r domain.FinishReason) string {
	switch r {
	case domain.FinishStop, domain.FinishRefusal:
		return "stop"
	case domain.FinishLength:
		return "length"
	case domain.FinishToolCalls:
		return "tool_calls"
	case domain.FinishContentFilter, domain.FinishSafety, domain.FinishRecitation:
		return "content_filter"
	default:
		return "stop"
	}
}

// DecodeUsage converts wire usage to canonical.
func DecodeUsage(w *wireUsage) domain.TokenUsage {
	u := domain.TokenUsage{
		InputTokens:  w.PromptTokens,
		OutputTokens: w.CompletionTokens,
		TotalTokens:  w.TotalTokens,
	}
	if u.TotalTokens == 0 {
		u = u.WithTotalDerivation()
	}
	if w.PromptTokensDetails != nil {
		u.CacheReadTokens = w.PromptTokensDetails.CachedTokens
	}
	if w.CompletionDetails != nil {
		u.ReasoningTokens = w.CompletionDetails.ReasoningTokens
	}
	return u.Sanitize()
}

func encodeUsagePtr(u domain.TokenUsage) *wireUsage {
	w := &wireUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	if w.TotalTokens == 0 {
		w.TotalTokens = w.PromptTokens + w.CompletionTokens
	}
	if u.CacheReadTokens > 0 {
		w.PromptTokensDetails = &wirePromptTokensDetails{CachedTokens: u.CacheReadTokens}
	}
	if u.ReasoningTokens > 0 {
		w.CompletionDetails = &wireCompletionDetails{ReasoningTokens: u.ReasoningTokens}
	}
	return w
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
