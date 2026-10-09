package mockprovider

// Deterministic upstream scenarios selected by the provider-side model
// name. The parity corpus (test/parity/corpus) drives upstream failures
// through the gateway without direct provider access: the gateway
// translates the canonical model to a provider model whose name encodes
// the behavior (e.g. "sc-500", "sc-delay-250", "sc-midstream").
//
// Model-name conventions (prefix "sc-"):
//
//      sc-NNN          respond with HTTP NNN in the protocol's native error
//                      envelope (e.g. sc-500, sc-401, sc-429)
//      sc-429rr        429 plus Retry-After: 7
//      sc-ctxlen       400 context-length-exceeded error (native code)
//      sc-stall        sleep 30s (until client context dies)
//      sc-delay-NNN    sleep NNN ms, then the normal echo response
//      sc-midstream    stream: emit the first frame, then abort the
//                      connection (mid-stream failure after first byte)
//      sc-badjson      200 with an invalid JSON body
//      sc-toolstream   OpenAI only: tool-call streaming event order
//      sc-thinkstream  Anthropic only: thinking + signature deltas
//      sc-ping         Anthropic only: ping frame between events
//
// Scenario names are case-sensitive; any other model (including ones
// merely containing "sc-") takes the normal echo path.
import (
        "fmt"
        "net/http"
        "strconv"
        "strings"
        "time"
)

// scenarioKind enumerates the supported behaviors.
type scenarioKind int

const (
        scNone scenarioKind = iota
        scStatus
        scCtxLen
        scStall
        scDelay
        scMidstream
        scBadJSON
        scToolStream
        scThinkStream
        scPing
)

// scenario is a parsed model-name directive.
type scenario struct {
        kind       scenarioKind
        status     int
        retryAfter int
        delayMS    int
}

// parseScenario interprets a provider model name. ok is false for every
// name without a recognized "sc-" directive.
func parseScenario(model string) (scenario, bool) {
        rest, ok := strings.CutPrefix(model, "sc-")
        if !ok {
                return scenario{}, false
        }
        switch {
        case rest == "429rr":
                return scenario{kind: scStatus, status: 429, retryAfter: 7}, true
        case rest == "ctxlen":
                return scenario{kind: scCtxLen}, true
        case rest == "stall":
                return scenario{kind: scStall}, true
        case rest == "midstream":
                return scenario{kind: scMidstream}, true
        case rest == "badjson":
                return scenario{kind: scBadJSON}, true
        case rest == "toolstream":
                return scenario{kind: scToolStream}, true
        case rest == "thinkstream":
                return scenario{kind: scThinkStream}, true
        case rest == "ping":
                return scenario{kind: scPing}, true
        case strings.HasPrefix(rest, "delay-"):
                ms, err := strconv.Atoi(rest[len("delay-"):])
                if err != nil || ms < 0 {
                        return scenario{}, false
                }
                return scenario{kind: scDelay, delayMS: ms}, true
        case len(rest) == 3:
                code, err := strconv.Atoi(rest)
                if err != nil || code < 400 || code > 599 {
                        return scenario{}, false
                }
                return scenario{kind: scStatus, status: code}, true
        }
        return scenario{}, false
}

// applyScenario executes non-streaming scenarios. It reports whether the
// response was fully written (caller must return) — false means "not
// handled, continue the normal path" (unknown scenario, or a
// streaming-only scenario on a non-streaming request, which falls back
// to the normal echo so behavior stays defined).
func applyScenario(w http.ResponseWriter, r *http.Request, protocol, model string, sc scenario) bool {
        switch sc.kind {
        case scStatus:
                renderScenarioError(w, protocol, sc.status, sc.retryAfter)
                return true
        case scCtxLen:
                renderContextLengthError(w, protocol)
                return true
        case scStall:
                select {
                case <-time.After(30 * time.Second):
                        renderScenarioError(w, protocol, http.StatusGatewayTimeout, 0)
                        return true
                case <-r.Context().Done():
                        return true
                }
        case scBadJSON:
                w.Header().Set("Content-Type", "application/json")
                w.WriteHeader(http.StatusOK)
                _, _ = fmt.Fprint(w, `{"not valid`)
                return true
        case scDelay:
                select {
                case <-time.After(time.Duration(sc.delayMS) * time.Millisecond):
                        return false
                case <-r.Context().Done():
                        return true
                }
        case scMidstream, scToolStream, scThinkStream, scPing:
                // Streaming-only behaviors; non-streaming requests take the
                // normal echo path.
                return false
        default:
                return false
        }
}

// renderScenarioError writes a native error envelope for the protocol.
func renderScenarioError(w http.ResponseWriter, protocol string, status, retryAfter int) {
        if retryAfter > 0 {
                w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
        }
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        switch protocol {
        case "anthropic":
                if status == 529 {
                        // Anthropic overload: 529 with overloaded_error body.
                        _, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
                        return
                }
                fmt.Fprintf(w, `{"type":"error","error":{"type":"api_error","message":"mock anthropic error %d"}}`, status)
        case "gemini":
                fmt.Fprintf(w, `{"error":{"code":%d,"message":"mock gemini error %d","status":"INTERNAL"}}`, status, status)
        default: // openai
                fmt.Fprintf(w, `{"error":{"message":"mock openai error %d","type":"api_error","code":"mock_error"}}`, status)
        }
}

// renderContextLengthError writes the native 400 context-length error.
func renderContextLengthError(w http.ResponseWriter, protocol string) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusBadRequest)
        switch protocol {
        case "anthropic":
                _, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 5000 tokens > 4096 maximum"}}`)
        case "gemini":
                _, _ = fmt.Fprint(w, `{"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens permitted (4096).","status":"INVALID_ARGUMENT"}}`)
        default: // openai
                _, _ = fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 4096 tokens. However, your messages resulted in 5000 tokens. Please reduce the length of the messages.","type":"invalid_request_error","code":"context_length_exceeded","param":"messages"}}`)
        }
}

// modelFromTarget splits a Gemini "{model}:{method}" path tail at the
// last colon (methods never contain colons; some model ids do).
func modelFromTarget(target string) string {
        i := strings.LastIndex(target, ":")
        if i <= 0 {
                return ""
        }
        return target[:i]
}

// applyStreamScenario executes streaming-only scenarios. It reports
// whether the response was fully written. Non-streaming kinds (status,
// ctxlen, badjson, stall, delay) also apply here: error statuses answer
// stream requests with a plain error response, which is how real
// providers fail stream requests before any event is emitted.
func applyStreamScenario(w http.ResponseWriter, r *http.Request, protocol string, sc scenario) bool {
        switch sc.kind {
        case scStatus, scCtxLen, scBadJSON:
                return applyScenario(w, r, protocol, "", sc)
        case scStall:
                select {
                case <-time.After(30 * time.Second):
                        return true
                case <-r.Context().Done():
                        return true
                }
        case scDelay:
                select {
                case <-time.After(time.Duration(sc.delayMS) * time.Millisecond):
                        return false
                case <-r.Context().Done():
                        return true
                }
        case scMidstream:
                writeStreamHeaders(w)
                flusher, _ := w.(http.Flusher)
                switch protocol {
                case "anthropic":
                        _, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_mock\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"sc-midstream\",\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\n")
                case "gemini":
                        _, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]},\"index\":0}],\"modelVersion\":\"sc-midstream\"}\n\n")
                default: // openai
                        _, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"sc-midstream\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
                }
                if flusher != nil {
                        flusher.Flush()
                }
                return true // abort: connection ends mid-stream
        case scToolStream:
                if protocol != "openai" {
                        return false
                }
                writeStreamHeaders(w)
                flusher, _ := w.(http.Flusher)
                _, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"sc-toolstream\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":null,\"tool_calls\":[{\"index\":0,\"id\":\"call_mock_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]}}]}\n\n")
                _, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"sc-toolstream\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":null,\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}]}}]}\n\n")
                _, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"sc-toolstream\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":18,\"completion_tokens\":9,\"total_tokens\":27}}\n\n")
                _, _ = fmt.Fprint(w, "data: [DONE]\n\n")
                if flusher != nil {
                        flusher.Flush()
                }
                return true
        case scThinkStream:
                if protocol != "anthropic" {
                        return false
                }
                writeStreamHeaders(w)
                flusher, _ := w.(http.Flusher)
                _, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_mock\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"sc-thinkstream\",\"usage\":{\"input_tokens\":20,\"output_tokens\":1}}}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Let me think.\"}}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig-mock\"}}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"The answer is 42.\"}}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
                _, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":12}}\n\n")
                _, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
                if flusher != nil {
                        flusher.Flush()
                }
                return true
        case scPing:
                if protocol != "anthropic" {
                        return false
                }
                writeStreamHeaders(w)
                flusher, _ := w.(http.Flusher)
                _, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_mock\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"sc-ping\",\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\n")
                _, _ = fmt.Fprint(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello from mock Anthropic\"}}\n\n")
                _, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
                _, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":6}}\n\n")
                _, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
                if flusher != nil {
                        flusher.Flush()
                }
                return true
        default:
                return false
        }
}

// writeStreamHeaders starts an SSE response.
func writeStreamHeaders(w http.ResponseWriter) {
        w.Header().Set("Content-Type", "text/event-stream")
        w.Header().Set("Cache-Control", "no-cache")
        w.Header().Set("Connection", "keep-alive")
        w.WriteHeader(http.StatusOK)
}
