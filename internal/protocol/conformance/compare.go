package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// ---------------------------------------------------------------------------
// Semantic comparators: canonical forms compared modulo documented losses
// ---------------------------------------------------------------------------

// DiffRequests compares two canonical requests semantically, ignoring the
// fields named in the ignore set (lossy keys from lossy.go). It returns ""
// when equal.
func DiffRequests(want, got domain.Request, ignore map[string]bool) string {
	var diffs []string

	if !ignore[IgModel] && want.Model != got.Model {
		diffs = append(diffs, fmt.Sprintf("model: %q vs %q", want.Model, got.Model))
	}
	if !ignore[IgStream] && want.Stream != got.Stream {
		diffs = append(diffs, fmt.Sprintf("stream: %v vs %v", want.Stream, got.Stream))
	}
	if !ignore[IgUser] && want.User != got.User {
		diffs = append(diffs, fmt.Sprintf("user: %q vs %q", want.User, got.User))
	}

	// Messages (system folding only affects ordering of leading system
	// messages, which the fixtures keep in position).
	wantMsgs, gotMsgs := foldSystem(want.Messages), foldSystem(got.Messages)
	if ignore[IgSystemFold] {
		// Compare roles/content only, ignoring system message positions.
		wantMsgs, gotMsgs = dropSystem(want.Messages), dropSystem(got.Messages)
	}
	if len(wantMsgs) != len(gotMsgs) {
		diffs = append(diffs, fmt.Sprintf("message count: %d vs %d", len(wantMsgs), len(gotMsgs)))
	} else {
		for i := range wantMsgs {
			if wantMsgs[i].Role != gotMsgs[i].Role {
				diffs = append(diffs, fmt.Sprintf("message %d role: %s vs %s", i, wantMsgs[i].Role, gotMsgs[i].Role))
				continue
			}
			if d := diffBlocks(wantMsgs[i].Content, gotMsgs[i].Content, ignore, fmt.Sprintf("msg %d", i)); d != "" {
				diffs = append(diffs, d)
			}
		}
	}

	// Tools.
	if len(want.Tools) != len(got.Tools) {
		diffs = append(diffs, fmt.Sprintf("tool count: %d vs %d", len(want.Tools), len(got.Tools)))
	} else {
		for i := range want.Tools {
			if want.Tools[i].Name != got.Tools[i].Name {
				diffs = append(diffs, fmt.Sprintf("tool %d name: %s vs %s", i, want.Tools[i].Name, got.Tools[i].Name))
			}
			if want.Tools[i].Description != got.Tools[i].Description {
				diffs = append(diffs, fmt.Sprintf("tool %d description differs", i))
			}
			if !jsonEqual(want.Tools[i].InputSchema, got.Tools[i].InputSchema) {
				diffs = append(diffs, fmt.Sprintf("tool %d schema: %s vs %s", i, want.Tools[i].InputSchema, got.Tools[i].InputSchema))
			}
		}
	}

	// Tool choice.
	switch {
	case want.ToolChoice == nil && got.ToolChoice == nil:
	case want.ToolChoice == nil || got.ToolChoice == nil:
		diffs = append(diffs, fmt.Sprintf("tool_choice: %+v vs %+v", want.ToolChoice, got.ToolChoice))
	case want.ToolChoice.Mode != got.ToolChoice.Mode || want.ToolChoice.Name != got.ToolChoice.Name:
		diffs = append(diffs, fmt.Sprintf("tool_choice: %+v vs %+v", want.ToolChoice, got.ToolChoice))
	}

	// Sampling.
	diffs = append(diffs, diffSampling(want.Sampling, got.Sampling, ignore)...)

	if len(diffs) > 0 {
		return "request diverged:\n  " + strings.Join(diffs, "\n  ")
	}
	return ""
}

func diffSampling(want, got domain.SamplingParams, ignore map[string]bool) []string {
	var diffs []string
	if want.MaxTokens != got.MaxTokens {
		diffs = append(diffs, fmt.Sprintf("max_tokens: %d vs %d", want.MaxTokens, got.MaxTokens))
	}
	if !floatPtrEqual(want.Temperature, got.Temperature) {
		diffs = append(diffs, fmt.Sprintf("temperature: %v vs %v", want.Temperature, got.Temperature))
	}
	if !floatPtrEqual(want.TopP, got.TopP) {
		diffs = append(diffs, fmt.Sprintf("top_p: %v vs %v", want.TopP, got.TopP))
	}
	if !ignore[IgTopK] && !intPtrEqual(want.TopK, got.TopK) {
		diffs = append(diffs, fmt.Sprintf("top_k: %v vs %v", want.TopK, got.TopK))
	}
	if !stringsEqual(want.StopSequences, got.StopSequences) {
		diffs = append(diffs, fmt.Sprintf("stop_sequences: %v vs %v", want.StopSequences, got.StopSequences))
	}
	if !ignore[IgSeed] && !intPtrEqual(want.Seed, got.Seed) {
		diffs = append(diffs, fmt.Sprintf("seed: %v vs %v", want.Seed, got.Seed))
	}
	if !ignore[IgLogitBias] && !mapsEqual(want.LogitBias, got.LogitBias) {
		diffs = append(diffs, fmt.Sprintf("logit_bias: %v vs %v", want.LogitBias, got.LogitBias))
	}
	if !ignore[IgLogprobs] && (want.Logprobs != got.Logprobs || want.TopLogprobs != got.TopLogprobs) {
		diffs = append(diffs, "logprobs flags differ")
	}
	if !ignore[IgResponseFormat] {
		switch {
		case want.ResponseFormat == nil && got.ResponseFormat == nil:
		case want.ResponseFormat == nil || got.ResponseFormat == nil:
			diffs = append(diffs, fmt.Sprintf("response_format: %+v vs %+v", want.ResponseFormat, got.ResponseFormat))
		default:
			if ignore[IgJSONSchemaEnv] {
				// compare only the inner schema presence
				if (want.ResponseFormat.JSONSchema == nil) != (got.ResponseFormat.JSONSchema == nil) {
					diffs = append(diffs, "response_format schema presence differs")
				}
			} else if !jsonEqual(want.ResponseFormat.JSONSchema, got.ResponseFormat.JSONSchema) {
				diffs = append(diffs, "response_format schema differs")
			}
		}
	}
	return diffs
}

func diffBlocks(want, got []domain.ContentBlock, ignore map[string]bool, where string) string {
	if ignore[IgThinkingHistory] {
		want = filterThinking(want)
		got = filterThinking(got)
	}
	if len(want) != len(got) {
		return fmt.Sprintf("%s: block count %d vs %d (want %+v / got %+v)", where, len(want), len(got), want, got)
	}
	var diffs []string
	for i := range want {
		w, g := want[i], got[i]
		if w.Type != g.Type {
			diffs = append(diffs, fmt.Sprintf("%s block %d: type %s vs %s", where, i, w.Type, g.Type))
			continue
		}
		switch w.Type {
		case domain.BlockText:
			if w.Text != g.Text {
				diffs = append(diffs, fmt.Sprintf("%s block %d text: %q vs %q", where, i, w.Text, g.Text))
			}
		case domain.BlockThinking:
			if w.Text != g.Text {
				diffs = append(diffs, fmt.Sprintf("%s block %d thinking: %q vs %q", where, i, w.Text, g.Text))
			}
		case domain.BlockImage:
			if w.Image.MimeType != g.Image.MimeType || w.Image.Base64 != g.Image.Base64 {
				diffs = append(diffs, fmt.Sprintf("%s block %d image: %+v vs %+v", where, i, w.Image, g.Image))
			}
			if !ignore[IgImageDetail] && w.Image.Detail != g.Image.Detail {
				diffs = append(diffs, fmt.Sprintf("%s block %d image detail differs", where, i))
			}
		case domain.BlockToolCall:
			if w.Call.Name != g.Call.Name {
				diffs = append(diffs, fmt.Sprintf("%s block %d call name: %s vs %s", where, i, w.Call.Name, g.Call.Name))
			}
			if !jsonEqual([]byte(w.Call.Arguments), []byte(g.Call.Arguments)) {
				diffs = append(diffs, fmt.Sprintf("%s block %d call args: %s vs %s", where, i, w.Call.Arguments, g.Call.Arguments))
			}
			if !ignore[IgToolCallIDs] && w.Call.ID != g.Call.ID {
				diffs = append(diffs, fmt.Sprintf("%s block %d call id: %s vs %s", where, i, w.Call.ID, g.Call.ID))
			}
		case domain.BlockToolResult:
			if !stringsEqual(blockTexts(w.Tool.Content), blockTexts(g.Tool.Content)) {
				diffs = append(diffs, fmt.Sprintf("%s block %d result content differs", where, i))
			}
			if w.Tool.IsError != g.Tool.IsError {
				diffs = append(diffs, fmt.Sprintf("%s block %d result is_error differs", where, i))
			}
			// Identity: ID when both sides carry real IDs; name otherwise.
			if !ignore[IgToolCallIDs] && w.Tool.CallID != g.Tool.CallID {
				diffs = append(diffs, fmt.Sprintf("%s block %d result call id: %s vs %s", where, i, w.Tool.CallID, g.Tool.CallID))
			}
			if w.Tool.Name != "" && g.Tool.Name != "" && w.Tool.Name != g.Tool.Name {
				diffs = append(diffs, fmt.Sprintf("%s block %d result name: %s vs %s", where, i, w.Tool.Name, g.Tool.Name))
			}
		}
	}
	if len(diffs) > 0 {
		return strings.Join(diffs, "\n  ")
	}
	return ""
}

// DiffResponses compares canonical responses modulo the ignore set.
func DiffResponses(want, got domain.Response, ignore map[string]bool) string {
	var diffs []string
	if want.ID != got.ID {
		diffs = append(diffs, fmt.Sprintf("id: %q vs %q", want.ID, got.ID))
	}
	if want.Model != got.Model {
		diffs = append(diffs, fmt.Sprintf("model: %q vs %q", want.Model, got.Model))
	}
	if want.FinishReason != got.FinishReason {
		diffs = append(diffs, fmt.Sprintf("finish: %s vs %s", want.FinishReason, got.FinishReason))
	}
	if d := diffBlocks(want.Content, got.Content, ignore, "response"); d != "" {
		diffs = append(diffs, d)
	}
	diffs = append(diffs, diffUsage(want.Usage, got.Usage, ignore)...)
	if !ignore[IgCreated] && want.CreatedMS != got.CreatedMS {
		diffs = append(diffs, fmt.Sprintf("created: %d vs %d", want.CreatedMS, got.CreatedMS))
	}
	if len(diffs) > 0 {
		return "response diverged:\n  " + strings.Join(diffs, "\n  ")
	}
	return ""
}

func diffUsage(want, got domain.TokenUsage, ignore map[string]bool) []string {
	var diffs []string
	if want.InputTokens != got.InputTokens {
		diffs = append(diffs, fmt.Sprintf("usage.input: %d vs %d", want.InputTokens, got.InputTokens))
	}
	if want.OutputTokens != got.OutputTokens {
		diffs = append(diffs, fmt.Sprintf("usage.output: %d vs %d", want.OutputTokens, got.OutputTokens))
	}
	if want.TotalTokens != got.TotalTokens {
		diffs = append(diffs, fmt.Sprintf("usage.total: %d vs %d", want.TotalTokens, got.TotalTokens))
	}
	if !ignore[IgReasoningUsage] && want.ReasoningTokens != got.ReasoningTokens {
		diffs = append(diffs, fmt.Sprintf("usage.reasoning: %d vs %d", want.ReasoningTokens, got.ReasoningTokens))
	}
	if want.CacheReadTokens != got.CacheReadTokens {
		diffs = append(diffs, fmt.Sprintf("usage.cache_read: %d vs %d", want.CacheReadTokens, got.CacheReadTokens))
	}
	if !ignore[IgCacheWriteUsage] && want.CacheWriteTokens != got.CacheWriteTokens {
		diffs = append(diffs, fmt.Sprintf("usage.cache_write: %d vs %d", want.CacheWriteTokens, got.CacheWriteTokens))
	}
	return diffs
}

// AssembledStream is the semantic reassembly of a canonical event sequence:
// the full blocks, the terminal finish reason, and the final usage.
type AssembledStream struct {
	Blocks []domain.ContentBlock
	Finish domain.FinishReason
	Usage  domain.TokenUsage
	ID     string
	Model  string
}

// AssembleStream folds canonical events into their semantic payload.
func AssembleStream(events []domain.StreamEvent) AssembledStream {
	var out AssembledStream
	blocks := map[int]*domain.ContentBlock{}
	var order []int

	for _, ev := range events {
		switch ev.Type {
		case domain.EventMessageStart:
			out.ID, out.Model = ev.ID, ev.Model
			if ev.Usage != nil {
				out.Usage = out.Usage.Add(*ev.Usage)
			}
		case domain.EventBlockStart:
			blk := domain.ContentBlock{}
			if ev.Block != nil {
				blk = *ev.Block
			}
			blocks[ev.Index] = &blk
			order = append(order, ev.Index)
		case domain.EventBlockDelta:
			blk := blocks[ev.Index]
			if blk == nil {
				continue
			}
			switch {
			case ev.TextDelta != "":
				blk.Text += ev.TextDelta
			case ev.ThinkingDelta != "":
				blk.Text += ev.ThinkingDelta
			case ev.SignatureDelta != "":
				blk.Signature += ev.SignatureDelta
			case ev.ArgumentsDelta != "":
				if blk.Call == nil {
					blk.Call = &domain.ToolCall{}
				}
				blk.Call.Arguments += ev.ArgumentsDelta
			}
		case domain.EventMessageDelta:
			if ev.FinishReason != "" {
				out.Finish = ev.FinishReason
			}
			if ev.Usage != nil {
				// Final usage is authoritative and cumulative.
				out.Usage = *ev.Usage
			}
		}
	}
	for _, idx := range order {
		out.Blocks = append(out.Blocks, *blocks[idx])
	}
	return out
}

// DiffStreams compares two canonical event sequences at the assembled
// semantic level (blocks, finish, usage).
func DiffStreams(want, got []domain.StreamEvent, ignore map[string]bool) string {
	w, g := AssembleStream(want), AssembleStream(got)
	var diffs []string
	if w.ID != g.ID {
		diffs = append(diffs, fmt.Sprintf("id: %q vs %q", w.ID, g.ID))
	}
	if w.Model != g.Model {
		diffs = append(diffs, fmt.Sprintf("model: %q vs %q", w.Model, g.Model))
	}
	if w.Finish != g.Finish {
		diffs = append(diffs, fmt.Sprintf("finish: %s vs %s", w.Finish, g.Finish))
	}
	if d := diffBlocks(w.Blocks, g.Blocks, ignore, "stream"); d != "" {
		diffs = append(diffs, d)
	}
	diffs = append(diffs, diffUsage(w.Usage, g.Usage, ignore)...)
	if len(diffs) > 0 {
		return "stream diverged:\n  " + strings.Join(diffs, "\n  ")
	}
	return ""
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func foldSystem(msgs []domain.Message) []domain.Message { return msgs } // fixtures keep system in position

func dropSystem(msgs []domain.Message) []domain.Message {
	var out []domain.Message
	for _, m := range msgs {
		if m.Role != domain.RoleSystem {
			out = append(out, m)
		}
	}
	return out
}

func filterThinking(blocks []domain.ContentBlock) []domain.ContentBlock {
	var out []domain.ContentBlock
	for _, b := range blocks {
		if b.Type != domain.BlockThinking {
			out = append(out, b)
		}
	}
	return out
}

func blockTexts(blocks []domain.ContentBlock) []string {
	var out []string
	for _, b := range blocks {
		out = append(out, b.Text)
	}
	return out
}

func floatPtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func intPtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func jsonEqual(a, b []byte) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	na, errA := normalizeJSON(a)
	nb, errB := normalizeJSON(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(na, nb)
}

func normalizeJSON(b []byte) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
