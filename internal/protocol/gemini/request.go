package gemini

import (
	"encoding/json"
	"fmt"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// callID synthesizes the deterministic tool-call ID Gemini does not carry
// on its wire (mapping doc §3.1: "call_{name}")
// [quirk:gemini-no-call-ids].
func callID(name string) string { return "call_" + name }

// DecodeRequest parses a Gemini generateContent body into canonical form.
func DecodeRequest(body []byte) (domain.Request, error) {
	var w generateRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return domain.Request{}, fmt.Errorf("gemini: bad request body: %w", err)
	}

	req := domain.Request{Messages: nil}

	// systemInstruction → leading system message.
	if w.SystemInstruction != nil && len(w.SystemInstruction.Parts) > 0 {
		var blocks []domain.ContentBlock
		for _, p := range w.SystemInstruction.Parts {
			if p.Text != "" && !p.Thought {
				blocks = append(blocks, domain.ContentBlock{Type: domain.BlockText, Text: p.Text})
			}
		}
		if len(blocks) > 0 {
			req.Messages = append(req.Messages, domain.Message{Role: domain.RoleSystem, Content: blocks})
		}
	}

	for i, c := range w.Contents {
		role := domain.RoleUser
		if c.Role == "model" {
			role = domain.RoleAssistant
		}
		msg := domain.Message{Role: role}
		for _, p := range c.Parts {
			blocks, err := decodePart(p)
			if err != nil {
				return domain.Request{}, fmt.Errorf("gemini: content %d: %w", i, err)
			}
			msg.Content = append(msg.Content, blocks...)
		}
		req.Messages = append(req.Messages, msg)
	}

	for _, t := range w.Tools {
		for _, fd := range t.FunctionDeclarations {
			schema := fd.Parameters
			if len(schema) == 0 {
				schema = fd.ParametersJSONSchema
			}
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			req.Tools = append(req.Tools, domain.Tool{
				Name: fd.Name, Description: fd.Description, InputSchema: schema,
			})
		}
	}

	if tc := w.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fcc := tc.FunctionCallingConfig
		switch fcc.Mode {
		case "AUTO", "":
			req.ToolChoice = &domain.ToolChoice{Mode: domain.ToolChoiceAuto}
		case "NONE":
			req.ToolChoice = &domain.ToolChoice{Mode: domain.ToolChoiceNone}
		case "ANY":
			// ANY + exactly one allowed name inverts to named choice.
			if len(fcc.AllowedFunctionNames) == 1 {
				req.ToolChoice = &domain.ToolChoice{Mode: domain.ToolChoiceNamed, Name: fcc.AllowedFunctionNames[0]}
			} else {
				req.ToolChoice = &domain.ToolChoice{Mode: domain.ToolChoiceRequired}
			}
		default:
			return domain.Request{}, fmt.Errorf("gemini: unknown functionCallingConfig mode %q", fcc.Mode)
		}
	}

	if gc := w.GenerationConfig; gc != nil {
		req.Sampling = domain.SamplingParams{
			MaxTokens:     gc.MaxOutputTokens,
			Temperature:   gc.Temperature,
			TopP:          gc.TopP,
			TopK:          gc.TopK,
			StopSequences: gc.StopSequences,
			Seed:          gc.Seed,
		}
		switch gc.ResponseMimeType {
		case "application/json":
			req.Sampling.ResponseFormat = &domain.ResponseFormat{Type: "json_object"}
		}
		if len(gc.ResponseSchema) > 0 {
			req.Sampling.ResponseFormat = &domain.ResponseFormat{
				Type:       "json_schema",
				JSONSchema: gc.ResponseSchema,
			}
		}
	}

	return req, nil
}

func decodePart(p wirePart) ([]domain.ContentBlock, error) {
	switch {
	case p.FunctionCall != nil:
		id := p.FunctionCall.ID
		if id == "" {
			id = callID(p.FunctionCall.Name)
		}
		args := string(p.FunctionCall.Args)
		if args == "" || args == "null" {
			args = "{}"
		}
		return []domain.ContentBlock{{
			Type: domain.BlockToolCall,
			Call: &domain.ToolCall{ID: id, Name: p.FunctionCall.Name, Arguments: args},
		}}, nil
	case p.FunctionResponse != nil:
		text, isError := responseToText(p.FunctionResponse.Response)
		id := p.FunctionResponse.ID
		if id == "" {
			id = callID(p.FunctionResponse.Name)
		}
		return []domain.ContentBlock{{
			Type: domain.BlockToolResult,
			Tool: &domain.ToolResult{
				CallID:  id,
				Name:    p.FunctionResponse.Name,
				Content: []domain.ContentBlock{{Type: domain.BlockText, Text: text}},
				IsError: isError,
			},
		}}, nil
	case p.InlineData != nil:
		return []domain.ContentBlock{{
			Type:  domain.BlockImage,
			Image: &domain.ImageContent{MimeType: p.InlineData.MimeType, Base64: p.InlineData.Data},
		}}, nil
	case p.Thought:
		return []domain.ContentBlock{{
			Type:      domain.BlockThinking,
			Text:      p.Text,
			Signature: p.ThoughtSignature,
		}}, nil
	default: // plain text
		if p.Text == "" {
			return nil, nil
		}
		return []domain.ContentBlock{{Type: domain.BlockText, Text: p.Text}}, nil
	}
}

// responseToText extracts the canonical text from a functionResponse
// object: {"result": "..."} → text; {"error": "..."} → error text.
func responseToText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return string(raw), false
	}
	if e, ok := m["error"]; ok {
		if s, ok := e.(string); ok {
			return s, true
		}
	}
	if r, ok := m["result"]; ok {
		switch v := r.(type) {
		case string:
			return v, false
		default:
			b, _ := json.Marshal(v)
			return string(b), false
		}
	}
	b, _ := json.Marshal(m)
	return string(b), false
}

// ---------------------------------------------------------------------------
// Encode: canonical → Gemini wire
// ---------------------------------------------------------------------------

// EncodeRequest renders a canonical request as a Gemini body. The model is
// NOT in the body: the proxy builds the path models/{model}:generateContent.
func EncodeRequest(req domain.Request) ([]byte, error) {
	w := generateRequest{}

	for _, m := range req.Messages {
		switch m.Role {
		case domain.RoleSystem:
			var parts []wirePart
			for _, b := range m.Content {
				if b.Type == domain.BlockText {
					parts = append(parts, wirePart{Text: b.Text})
				}
			}
			if len(parts) > 0 {
				w.SystemInstruction = &wireContent{Parts: parts}
			}
		default:
			role := "user"
			if m.Role == domain.RoleAssistant {
				role = "model"
			}
			var parts []wirePart
			for _, b := range m.Content {
				ps, err := encodePart(b)
				if err != nil {
					return nil, err
				}
				parts = append(parts, ps...)
			}
			if len(parts) == 0 {
				parts = []wirePart{{Text: ""}}
			}
			w.Contents = append(w.Contents, wireContent{Role: role, Parts: parts})
		}
	}

	// All canonical tools share one tools[] entry with N declarations
	// (the natural Gemini shape — mapping doc §3.1).
	if len(req.Tools) > 0 {
		declarations := make([]wireFunctionDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			declarations = append(declarations, wireFunctionDecl{
				Name: t.Name, Description: t.Description, Parameters: schema,
			})
		}
		w.Tools = []wireTool{{FunctionDeclarations: declarations}}
	}

	if tc := req.ToolChoice; tc != nil {
		fcc := &wireFnCallingConfig{}
		switch tc.Mode {
		case domain.ToolChoiceAuto:
			fcc.Mode = "AUTO"
		case domain.ToolChoiceNone:
			fcc.Mode = "NONE"
		case domain.ToolChoiceRequired:
			fcc.Mode = "ANY"
		case domain.ToolChoiceNamed:
			fcc.Mode = "ANY"
			fcc.AllowedFunctionNames = []string{tc.Name}
		}
		w.ToolConfig = &wireToolConfig{FunctionCallingConfig: fcc}
	}

	gc := &wireGenerationConfig{
		MaxOutputTokens: req.Sampling.MaxTokens,
		Temperature:     req.Sampling.Temperature,
		TopP:            req.Sampling.TopP,
		TopK:            req.Sampling.TopK,
		StopSequences:   req.Sampling.StopSequences,
		Seed:            req.Sampling.Seed,
	}
	if rf := req.Sampling.ResponseFormat; rf != nil {
		switch rf.Type {
		case "json_object":
			gc.ResponseMimeType = "application/json"
		case "json_schema":
			gc.ResponseMimeType = "application/json"
			// Extract the schema object from the OpenAI-style envelope
			// {"name", "schema", ...} (mapping doc §3.1).
			var env struct {
				Schema json.RawMessage `json:"schema"`
			}
			if err := json.Unmarshal(rf.JSONSchema, &env); err == nil && len(env.Schema) > 0 {
				gc.ResponseSchema = env.Schema
			} else {
				gc.ResponseSchema = rf.JSONSchema
			}
		}
	}
	w.GenerationConfig = gc

	return json.Marshal(w)
}

func encodePart(b domain.ContentBlock) ([]wirePart, error) {
	switch b.Type {
	case domain.BlockText:
		return []wirePart{{Text: b.Text}}, nil
	case domain.BlockImage:
		return []wirePart{{InlineData: &wireInlineData{
			MimeType: b.Image.MimeType, Data: b.Image.Base64,
		}}}, nil
	case domain.BlockToolCall:
		args := json.RawMessage(b.Call.Arguments)
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		if !json.Valid(args) {
			return nil, fmt.Errorf("gemini: tool arguments are not valid JSON: %s", b.Call.Arguments)
		}
		// The call ID is not carried: Gemini matches results by name
		// (mapping doc §3.1 lossy table).
		return []wirePart{{FunctionCall: &wireFunctionCall{Name: b.Call.Name, Args: args}}}, nil
	case domain.BlockToolResult:
		resp := map[string]any{"result": toolResultText(b.Tool)}
		if b.Tool.IsError {
			resp = map[string]any{"error": toolResultText(b.Tool)}
		}
		return []wirePart{{FunctionResponse: &wireFunctionResponse{
			Name:     toolResultName(b.Tool),
			Response: mustJSON(resp),
		}}}, nil
	case domain.BlockThinking:
		part := wirePart{Thought: true, Text: b.Text}
		if b.Signature != "" {
			part.ThoughtSignature = b.Signature
		}
		return []wirePart{part}, nil
	default:
		return nil, fmt.Errorf("gemini: unknown canonical block type %q", b.Type)
	}
}

// toolResultName recovers the function name: the explicit Name when the
// origin protocol carried it, else the synthesized "call_{name}" ID.
func toolResultName(tr *domain.ToolResult) string {
	if tr.Name != "" {
		return tr.Name
	}
	if tr.CallID == "" {
		return ""
	}
	const prefix = "call_"
	if len(tr.CallID) > len(prefix) && tr.CallID[:len(prefix)] == prefix {
		return tr.CallID[len(prefix):]
	}
	return tr.CallID
}

func toolResultText(tr *domain.ToolResult) string {
	out := ""
	for i, b := range tr.Content {
		if i > 0 {
			out += "\n"
		}
		out += b.Text
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
