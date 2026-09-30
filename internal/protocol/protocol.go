// Package protocol holds the wire-protocol adapters for OneGate.
//
// Layering contract (enforced by TestLayering in protocol_test.go and the
// Phase 2 gate):
//
//   - Packages under internal/protocol may import only the standard
//     library, internal/domain, sibling packages under internal/protocol,
//     and net/http for status rendering.
//   - They must never import internal/proxy, internal/routing,
//     internal/storage, internal/server, or any other subsystem —
//     adapters are pure translation functions.
//   - Provider-specific types never escape this tree: every boundary
//     crossing uses domain canonical types (ADR 004).
//
// Each adapter exposes the same shape of pure functions:
//
//	DecodeRequest / EncodeRequest       — body ⇄ domain.Request
//	DecodeResponse / EncodeResponse     — body ⇄ domain.Response
//	DecodeError / EncodeError           — error body ⇄ domain.GatewayError
//	StreamDecoder / StreamEncoder       — SSE ⇄ []domain.StreamEvent
//
// The proxy layer (Phase 3) composes these; adapters themselves perform no
// I/O beyond the byte slices handed to them.
package protocol
