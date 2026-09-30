package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// DecodeRequest parses an OpenAI chat-completions body into canonical form.
// Unknown fields are ignored; the legacy functions/function_call shape is
// folded into Tools/ToolChoice.
func DecodeRequest(body []byte) (domain.Request, error) {
	var w chatRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return domain.Request{}, fmt.Errorf("openai: bad request body: %w", err)
	}

	req := domain.Request{
		Model:  w.Model,
		Stream: w.Stream,
		User:   w.User,
		Sampling: domain.SamplingParams{
			MaxTokens:     w.MaxTokens,
			Temperature:   w.Temperature,
			TopP:          w.TopP,
			StopSequences: w.Stop,
			Seed:          w.Seed,
			LogitBias:     w.LogitBias,
			Logprobs:      w.Logprobs,
			TopLogprobs:   w.TopLogprobs,
		},
	}

	for _, t := range w.Tools {
		req.Tools = append(req.Tools, domain.Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}
	// Legacy functions → tools (mapping doc §1.1).
	for _, f := range w.Functions {
		req.Tools = append(req.Tools, domain.Tool{
			Name:        f.Name,
			Description: f.Description,
			InputSchema: f.Parameters,
		})
	}

	if w.ToolChoice != nil {
		tc, err := decodeToolChoice(w.ToolChoice)
		if err != nil {
			return domain.Request{}, err
		}
		req.ToolChoice = tc
	} else if w.FunctionCl != nil {
		// Legacy function_call: "auto"/"none"/{name}.
		var s string
		if err := json.Unmarshal(w.FunctionCl, &s); err == nil {
			req.ToolChoice = legacyChoiceToToolChoice(s, "")
		} else {
			var fc wireFnCall
			if err := json.Unmarshal(w.FunctionCl, &fc); err == nil && fc.Name != "" {
				req.ToolChoice = legacyChoiceToToolChoice("named", fc.Name)
			}
		}
	}

	if w.ResponseFmt != nil {
		req.Sampling.ResponseFormat = &domain.ResponseFormat{
			Type:       w.ResponseFmt.Type,
			JSONSchema: w.ResponseFmt.JSONSchema,
		}
	}

	// First pass: resolve tool-call IDs to function names so role:"tool"
	// messages (which carry only the ID) can fill ToolResult.Name for
	// protocols that need it (Gemini functionResponse).
	callNames := map[string]string{}
	for _, m := range w.Messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" && tc.Function != nil && tc.Function.Name != "" {
					callNames[tc.ID] = tc.Function.Name
				}
			}
			if m.FunctionCall != nil && m.FunctionCall.Name != "" {
				callNames["call_"+m.FunctionCall.Name] = m.FunctionCall.Name
			}
		}
	}

	for i, m := range w.Messages {
		msgs, err := decodeMessage(m, callNames)
		if err != nil {
			return domain.Request{}, fmt.Errorf("openai: message %d: %w", i, err)
		}
		req.Messages = append(req.Messages, msgs...)
	}
	return req, nil
}

func legacyChoiceToToolChoice(mode, name string) *domain.ToolChoice {
	switch mode {
	case "auto":
		return &domain.ToolChoice{Mode: domain.ToolChoiceAuto}
	case "none":
		return &domain.ToolChoice{Mode: domain.ToolChoiceNone}
	case "named":
		return &domain.ToolChoice{Mode: domain.ToolChoiceNamed, Name: name}
	default:
		return &domain.ToolChoice{Mode: domain.ToolChoiceAuto}
	}
}

func decodeToolChoice(raw json.RawMessage) (*domain.ToolChoice, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch domain.ToolChoiceMode(s) {
		case domain.ToolChoiceAuto, domain.ToolChoiceNone, domain.ToolChoiceRequired:
			return &domain.ToolChoice{Mode: domain.ToolChoiceMode(s)}, nil
		}
		return nil, fmt.Errorf("openai: unknown tool_choice %q", s)
	}
	var obj struct {
		Type     string `json:"type"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.Type != "function" || obj.Function == nil {
		return nil, fmt.Errorf("openai: bad tool_choice object: %s", raw)
	}
	return &domain.ToolChoice{Mode: domain.ToolChoiceNamed, Name: obj.Function.Name}, nil
}

// decodeMessage converts one wire message into zero, one, or more canonical
// messages (an OpenAI role:"tool" message becomes a user message holding a
// tool_result block).
func decodeMessage(m wireMessage, callNames map[string]string) ([]domain.Message, error) {
	switch m.Role {
	case "system", "developer":
		blocks, err := decodeContent(m.Content)
		if err != nil {
			return nil, err
		}
		return []domain.Message{{Role: domain.RoleSystem, Content: blocks, Name: m.Name}}, nil

	case "user":
		blocks, err := decodeContent(m.Content)
		if err != nil {
			return nil, err
		}
		return []domain.Message{{Role: domain.RoleUser, Content: blocks, Name: m.Name}}, nil

	case "assistant":
		var blocks []domain.ContentBlock
		text, err := decodeContent(m.Content)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, text...)
		for _, tc := range m.ToolCalls {
			args := ""
			if tc.Function != nil {
				args = tc.Function.Arguments
			}
			if args == "" {
				args = "{}"
			}
			blocks = append(blocks, domain.ContentBlock{
				Type: domain.BlockToolCall,
				Call: &domain.ToolCall{ID: tc.ID, Name: toolCallName(tc), Arguments: args},
			})
		}
		// Legacy function_call.
		if m.FunctionCall != nil {
			blocks = append(blocks, domain.ContentBlock{
				Type: domain.BlockToolCall,
				Call: &domain.ToolCall{ID: "call_" + m.FunctionCall.Name, Name: m.FunctionCall.Name, Arguments: m.FunctionCall.Arguments},
			})
		}
		return []domain.Message{{Role: domain.RoleAssistant, Content: blocks, Name: m.Name}}, nil

	case "tool", "function":
		text := extractText(m.Content)
		callID := m.ToolCallID
		if callID == "" {
			callID = m.Name // legacy role:"function" carries the name
		}
		return []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{
			Type: domain.BlockToolResult,
			Tool: &domain.ToolResult{
				CallID:  callID,
				Name:    callNames[callID],
				Content: []domain.ContentBlock{{Type: domain.BlockText, Text: text}},
			},
		}}}}, nil

	default:
		return nil, fmt.Errorf("openai: unknown role %q", m.Role)
	}
}

func toolCallName(tc wireToolCall) string {
	if tc.Function != nil {
		return tc.Function.Name
	}
	return ""
}

// decodeContent parses string | part[] | null content into blocks.
func decodeContent(raw json.RawMessage) ([]domain.ContentBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		return []domain.ContentBlock{{Type: domain.BlockText, Text: s}}, nil
	}
	var parts []wirePart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("bad content: %w", err)
	}
	var blocks []domain.ContentBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			blocks = append(blocks, domain.ContentBlock{Type: domain.BlockText, Text: p.Text})
		case "image_url":
			img := &domain.ImageContent{URL: p.ImageURL.URL, Detail: p.ImageURL.Detail}
			img.MimeType, img.Base64 = splitDataURL(p.ImageURL.URL)
			blocks = append(blocks, domain.ContentBlock{Type: domain.BlockImage, Image: img})
		default:
			// Unknown part types (audio, file) are dropped (ADR 004 exclusions).
		}
	}
	return blocks, nil
}

// splitDataURL extracts mime type and base64 payload from a data: URL.
// Non-data URLs return ("", "") — the URL stays in ImageContent.URL.
func splitDataURL(url string) (mime, b64 string) {
	const prefix = "data:"
	if !strings.HasPrefix(url, prefix) {
		return "", ""
	}
	rest := url[len(prefix):]
	semi := strings.Index(rest, ";base64,")
	if semi < 0 {
		return "", ""
	}
	return rest[:semi], rest[semi+len(";base64,"):]
}

// extractText flattens string content for tool messages.
func extractText(raw json.RawMessage) string {
	blocks, err := decodeContent(raw)
	if err != nil {
		return string(raw)
	}
	var sb strings.Builder
	for i, b := range blocks {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(b.Text)
		if b.Image != nil {
			sb.WriteString("[image]")
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Encode: canonical → OpenAI wire
// ---------------------------------------------------------------------------

// EncodeRequest renders a canonical request as an OpenAI body. The caller
// (proxy) sets req.Model to the provider-side model name before encoding.
func EncodeRequest(req domain.Request) ([]byte, error) {
	w := chatRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		MaxTokens:   req.Sampling.MaxTokens,
		Temperature: req.Sampling.Temperature,
		TopP:        req.Sampling.TopP,
		Stop:        req.Sampling.StopSequences,
		Seed:        req.Sampling.Seed,
		LogitBias:   req.Sampling.LogitBias,
		Logprobs:    req.Sampling.Logprobs,
		TopLogprobs: req.Sampling.TopLogprobs,
		User:        req.User,
	}

	for _, t := range req.Tools {
		w.Tools = append(w.Tools, wireTool{
			Type: "function",
			Function: wireFnSpec{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	if tc := req.ToolChoice; tc != nil {
		switch tc.Mode {
		case domain.ToolChoiceAuto, domain.ToolChoiceNone, domain.ToolChoiceRequired:
			w.ToolChoice = json.RawMessage(`"` + string(tc.Mode) + `"`)
		case domain.ToolChoiceNamed:
			w.ToolChoice = mustJSON(map[string]any{
				"type":     "function",
				"function": map[string]any{"name": tc.Name},
			})
		}
	}

	if rf := req.Sampling.ResponseFormat; rf != nil {
		w.ResponseFmt = &wireResponseFmt{Type: rf.Type, JSONSchema: rf.JSONSchema}
	}

	for _, m := range req.Messages {
		w.Messages = append(w.Messages, encodeMessage(m)...)
	}

	return json.Marshal(w)
}

// encodeMessage renders one canonical message as one or more wire
// messages. User messages carrying tool_result blocks expand into separate
// role:"tool" messages followed by a user message for the remaining
// blocks (mapping doc §1.1).
func encodeMessage(m domain.Message) []wireMessage {
	switch m.Role {
	case domain.RoleSystem:
		return []wireMessage{{
			Role:    "system",
			Content: mustJSON(joinText(m.Content)),
			Name:    m.Name,
		}}

	case domain.RoleAssistant:
		var out wireMessage
		out.Role = "assistant"
		out.Name = m.Name
		var texts []string
		var calls []wireToolCall
		for _, b := range m.Content {
			switch b.Type {
			case domain.BlockText:
				texts = append(texts, b.Text)
			case domain.BlockToolCall:
				calls = append(calls, wireToolCall{
					ID:   b.Call.ID,
					Type: "function",
					Function: &wireFnArg{
						Name:      b.Call.Name,
						Arguments: b.Call.Arguments,
					},
				})
			case domain.BlockThinking:
				// Dropped for OpenAI providers (mapping doc §1.1).
			case domain.BlockImage:
				// Assistant images are not a thing on this protocol.
			}
		}
		switch {
		case len(texts) > 0:
			out.Content = mustJSON(strings.Join(texts, "\n\n"))
		case len(calls) > 0:
			// OpenAI convention: content is null when only tool calls
			// [quirk:openai-content-null].
			out.Content = json.RawMessage(`null`)
		}
		out.ToolCalls = calls
		return []wireMessage{out}

	default: // user, tool
		var out []wireMessage
		var userBlocks []domain.ContentBlock
		for _, b := range m.Content {
			if b.Type == domain.BlockToolResult {
				out = append(out, wireMessage{
					Role:       "tool",
					ToolCallID: b.Tool.CallID,
					Content:    mustJSON(encodeToolResultText(b.Tool)),
				})
				continue
			}
			userBlocks = append(userBlocks, b)
		}
		if len(userBlocks) > 0 {
			out = append(out, wireMessage{Role: "user", Content: encodeContentRaw(userBlocks), Name: m.Name})
		}
		if len(out) == 0 {
			out = append(out, wireMessage{Role: "user", Content: mustJSON("")})
		}
		return out
	}
}

func encodeToolResultText(tr *domain.ToolResult) string {
	var sb strings.Builder
	if tr.IsError {
		sb.WriteString("Error: ")
	}
	for i, b := range tr.Content {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// encodeContentRaw renders message content: compact string form for
// text-only messages, part-array form as soon as an image appears
// (mapping doc §1.1).
func encodeContentRaw(blocks []domain.ContentBlock) json.RawMessage {
	allText := true
	for _, b := range blocks {
		if b.Type != domain.BlockText {
			allText = false
			break
		}
	}
	if allText {
		return mustJSON(joinText(blocks))
	}
	parts := make([]wirePart, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case domain.BlockText:
			parts = append(parts, wirePart{Type: "text", Text: b.Text})
		case domain.BlockImage:
			url := b.Image.URL
			if url == "" && b.Image.Base64 != "" {
				url = "data:" + b.Image.MimeType + ";base64," + b.Image.Base64
			}
			parts = append(parts, wirePart{Type: "image_url", ImageURL: &wireImageURL{URL: url, Detail: b.Image.Detail}})
		}
	}
	return mustJSON(parts)
}

func joinText(blocks []domain.ContentBlock) string {
	var texts []string
	for _, b := range blocks {
		if b.Type == domain.BlockText {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Only called on shapes that cannot fail (strings, part arrays).
		return json.RawMessage(`null`)
	}
	return b
}
