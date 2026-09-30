package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// defaultMaxTokens is applied when a canonical request carries no
// max_tokens: Anthropic requires the field (mapping doc §2.1)
// [quirk:anthropic-max-tokens-required].
const defaultMaxTokens = 4096

// DecodeRequest parses an Anthropic messages body into canonical form.
func DecodeRequest(body []byte) (domain.Request, error) {
	var w messagesRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return domain.Request{}, fmt.Errorf("anthropic: bad request body: %w", err)
	}

	req := domain.Request{
		Model:  w.Model,
		Stream: w.Stream,
		Sampling: domain.SamplingParams{
			MaxTokens:     w.MaxTokens,
			Temperature:   w.Temperature,
			TopP:          w.TopP,
			TopK:          w.TopK,
			StopSequences: w.StopSequences,
		},
	}

	// System: string or block array → system messages (leading).
	sysBlocks, err := decodeSystem(w.System)
	if err != nil {
		return domain.Request{}, err
	}
	if len(sysBlocks) > 0 {
		req.Messages = append(req.Messages, domain.Message{Role: domain.RoleSystem, Content: sysBlocks})
	}

	if w.Metadata != nil {
		req.User = w.Metadata.UserID
	}

	for _, t := range w.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		req.Tools = append(req.Tools, domain.Tool{
			Name: t.Name, Description: t.Description, InputSchema: schema,
		})
	}

	if len(w.ToolChoice) > 0 {
		var tc struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(w.ToolChoice, &tc); err != nil {
			return domain.Request{}, fmt.Errorf("anthropic: bad tool_choice: %w", err)
		}
		switch tc.Type {
		case "auto":
			req.ToolChoice = &domain.ToolChoice{Mode: domain.ToolChoiceAuto}
		case "any":
			req.ToolChoice = &domain.ToolChoice{Mode: domain.ToolChoiceRequired}
		case "none":
			req.ToolChoice = &domain.ToolChoice{Mode: domain.ToolChoiceNone}
		case "tool":
			req.ToolChoice = &domain.ToolChoice{Mode: domain.ToolChoiceNamed, Name: tc.Name}
		default:
			return domain.Request{}, fmt.Errorf("anthropic: unknown tool_choice type %q", tc.Type)
		}
	}

	// First pass: resolve tool_use IDs to names for ToolResult.Name.
	callNames := map[string]string{}
	for _, m := range w.Messages {
		if m.Role != "assistant" {
			continue
		}
		var blocks []wireBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.ID != "" && b.Name != "" {
				callNames[b.ID] = b.Name
			}
		}
	}

	for i, m := range w.Messages {
		blocks, err := decodeBlocks(m.Content)
		if err != nil {
			return domain.Request{}, fmt.Errorf("anthropic: message %d: %w", i, err)
		}
		for j := range blocks {
			if blocks[j].Tool != nil && blocks[j].Tool.Name == "" {
				blocks[j].Tool.Name = callNames[blocks[j].Tool.CallID]
			}
		}
		role := domain.RoleUser
		if m.Role == "assistant" {
			role = domain.RoleAssistant
		}
		req.Messages = append(req.Messages, domain.Message{Role: role, Content: blocks})
	}
	return req, nil
}

func decodeSystem(raw json.RawMessage) ([]domain.ContentBlock, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		return []domain.ContentBlock{{Type: domain.BlockText, Text: s}}, nil
	}
	return decodeBlocks(raw)
}

// decodeBlocks parses string | block[] content into canonical blocks.
func decodeBlocks(raw json.RawMessage) ([]domain.ContentBlock, error) {
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
	var blocks []wireBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("bad content: %w", err)
	}
	var out []domain.ContentBlock
	for _, b := range blocks {
		cb, err := decodeBlock(b)
		if err != nil {
			return nil, err
		}
		out = append(out, cb)
	}
	return out, nil
}

func decodeBlock(b wireBlock) (domain.ContentBlock, error) {
	switch b.Type {
	case "text":
		return domain.ContentBlock{Type: domain.BlockText, Text: b.Text}, nil
	case "image":
		if b.Source == nil || b.Source.Type != "base64" {
			return domain.ContentBlock{}, fmt.Errorf("anthropic: only base64 image sources supported, got %+v", b.Source)
		}
		return domain.ContentBlock{Type: domain.BlockImage, Image: &domain.ImageContent{
			MimeType: b.Source.MediaType, Base64: b.Source.Data,
		}}, nil
	case "tool_use":
		args := string(b.Input)
		if args == "" || args == "null" {
			args = "{}"
		}
		return domain.ContentBlock{Type: domain.BlockToolCall, Call: &domain.ToolCall{
			ID: b.ID, Name: b.Name, Arguments: args,
		}}, nil
	case "tool_result":
		content, err := decodeBlocks(b.Content)
		if err != nil {
			return domain.ContentBlock{}, fmt.Errorf("tool_result content: %w", err)
		}
		return domain.ContentBlock{Type: domain.BlockToolResult, Tool: &domain.ToolResult{
			CallID: b.ToolUseID, Content: content, IsError: b.IsError,
		}}, nil
	case "thinking":
		return domain.ContentBlock{Type: domain.BlockThinking, Text: b.Thinking, Signature: b.Signature}, nil
	case "redacted_thinking":
		return domain.ContentBlock{Type: domain.BlockThinking, Redacted: b.Data}, nil
	default:
		return domain.ContentBlock{}, fmt.Errorf("anthropic: unknown block type %q", b.Type)
	}
}

// ---------------------------------------------------------------------------
// Encode: canonical → Anthropic wire
// ---------------------------------------------------------------------------

// EncodeRequest renders a canonical request as an Anthropic body. System
// messages fold into the top-level system param (lossy: concatenated,
// mapping doc §2.1); tool_choice "none" drops the tools list entirely
// [quirk:anthropic-tool-choice-none].
func EncodeRequest(req domain.Request) ([]byte, error) {
	w := messagesRequest{
		Model:         req.Model,
		Stream:        req.Stream,
		MaxTokens:     req.Sampling.MaxTokens,
		Temperature:   req.Sampling.Temperature,
		TopP:          req.Sampling.TopP,
		TopK:          req.Sampling.TopK,
		StopSequences: req.Sampling.StopSequences,
	}
	if w.MaxTokens <= 0 {
		w.MaxTokens = defaultMaxTokens
	}
	if req.User != "" {
		w.Metadata = &wireMetadata{UserID: req.User}
	}

	var sysTexts []string
	for _, m := range req.Messages {
		switch m.Role {
		case domain.RoleSystem:
			for _, b := range m.Content {
				if b.Type == domain.BlockText {
					sysTexts = append(sysTexts, b.Text)
				}
			}
		default:
			role := "user"
			if m.Role == domain.RoleAssistant {
				role = "assistant"
			}
			blocks, err := encodeBlocks(m.Content)
			if err != nil {
				return nil, err
			}
			w.Messages = append(w.Messages, wireMessage{Role: role, Content: blocks})
		}
	}
	if len(sysTexts) > 0 {
		w.System = mustJSON(strings.Join(sysTexts, "\n\n"))
	}

	noneChoice := req.ToolChoice != nil && req.ToolChoice.Mode == domain.ToolChoiceNone
	if !noneChoice {
		for _, t := range req.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			w.Tools = append(w.Tools, wireTool{
				Name: t.Name, Description: t.Description, InputSchema: schema,
			})
		}
		if tc := req.ToolChoice; tc != nil {
			switch tc.Mode {
			case domain.ToolChoiceAuto:
				w.ToolChoice = mustJSON(map[string]any{"type": "auto"})
			case domain.ToolChoiceRequired:
				w.ToolChoice = mustJSON(map[string]any{"type": "any"})
			case domain.ToolChoiceNamed:
				w.ToolChoice = mustJSON(map[string]any{"type": "tool", "name": tc.Name})
			}
		}
	}

	return json.Marshal(w)
}

// encodeBlocks renders canonical blocks; single-text content compacts to a
// string, everything else uses the block array.
func encodeBlocks(blocks []domain.ContentBlock) (json.RawMessage, error) {
	if len(blocks) == 0 {
		return json.RawMessage(`""`), nil
	}
	if len(blocks) == 1 && blocks[0].Type == domain.BlockText {
		return mustJSON(blocks[0].Text), nil
	}
	var out []wireBlock
	for _, b := range blocks {
		wb, err := encodeBlock(b)
		if err != nil {
			return nil, err
		}
		out = append(out, wb)
	}
	return mustJSON(out), nil
}

func encodeBlock(b domain.ContentBlock) (wireBlock, error) {
	switch b.Type {
	case domain.BlockText:
		return wireBlock{Type: "text", Text: b.Text}, nil
	case domain.BlockImage:
		return wireBlock{Type: "image", Source: &wireImageSource{
			Type: "base64", MediaType: b.Image.MimeType, Data: b.Image.Base64,
		}}, nil
	case domain.BlockToolCall:
		input := json.RawMessage(b.Call.Arguments)
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		// Validate: arguments must be a JSON object (Anthropic requirement).
		if !json.Valid(input) {
			return wireBlock{}, fmt.Errorf("anthropic: tool arguments are not valid JSON: %s", b.Call.Arguments)
		}
		return wireBlock{Type: "tool_use", ID: b.Call.ID, Name: b.Call.Name, Input: input}, nil
	case domain.BlockToolResult:
		// tool_result content is always the block-array form for stability.
		var inner []wireBlock
		for _, cb := range b.Tool.Content {
			wb, err := encodeBlock(cb)
			if err != nil {
				return wireBlock{}, err
			}
			inner = append(inner, wb)
		}
		return wireBlock{Type: "tool_result", ToolUseID: b.Tool.CallID, Content: mustJSON(inner), IsError: b.Tool.IsError}, nil
	case domain.BlockThinking:
		if b.Redacted != "" {
			return wireBlock{Type: "redacted_thinking", Data: b.Redacted}, nil
		}
		return wireBlock{Type: "thinking", Thinking: b.Text, Signature: b.Signature}, nil
	default:
		return wireBlock{}, fmt.Errorf("anthropic: unknown canonical block type %q", b.Type)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}
