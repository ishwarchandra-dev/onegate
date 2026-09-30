package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// TestProtocolRoundTripConformance: the shared fixture, encoded to each
// protocol and decoded back, must yield the same semantic canonical form
// modulo that protocol's documented losses (acceptance: "Same semantic
// input yields semantically equal canonical forms").
func TestProtocolRoundTripConformance(t *testing.T) {
	base := CanonicalRequest()
	base.Sampling.LogitBias = map[string]int{"50256": -100}
	base.Sampling.Logprobs = true
	base.Sampling.TopLogprobs = 5
	base.User = "user-1"

	for _, proto := range Protocols {
		t.Run(proto, func(t *testing.T) {
			wire, err := EncodeRequest(proto, base)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := DecodeRequest(proto, wire)
			if err != nil {
				t.Fatalf("decode: %v\nwire: %s", err, wire)
			}
			if d := DiffRequests(base, got, LossKeysFor(proto)); d != "" {
				t.Fatalf("[%s] %s\nwire: %s", proto, d, wire)
			}
		})
	}
}

// TestCrossProtocolPairs runs every adapter pair (client protocol in,
// provider protocol out) and requires agreement on the common semantic
// core (acceptance: "Shared fixture set run through every adapter pair").
func TestCrossProtocolPairs(t *testing.T) {
	base := CanonicalRequest()
	base.Sampling.LogitBias = map[string]int{"50256": -100}
	base.Sampling.Logprobs = true
	base.User = "user-1"

	decoded := map[string]domain.Request{}
	for _, proto := range Protocols {
		wire, err := EncodeRequest(proto, base)
		if err != nil {
			t.Fatalf("encode %s: %v", proto, err)
		}
		got, err := DecodeRequest(proto, wire)
		if err != nil {
			t.Fatalf("decode %s: %v", proto, err)
		}
		decoded[proto] = got
	}

	for _, a := range Protocols {
		for _, b := range Protocols {
			if a == b {
				continue
			}
			t.Run(a+"→"+b, func(t *testing.T) {
				ignore := LossKeysFor(a)
				for k := range LossKeysFor(b) {
					ignore[k] = true
				}
				if d := DiffRequests(decoded[a], decoded[b], ignore); d != "" {
					t.Fatalf("pair %s/%s: %s", a, b, d)
				}
			})
		}
	}
}

// TestResponseConformance: the canonical response survives every encode/
// decode cycle modulo documented usage/timestamp losses.
func TestResponseConformance(t *testing.T) {
	base := CanonicalResponse()
	for _, proto := range Protocols {
		t.Run(proto, func(t *testing.T) {
			wire, err := EncodeResponse(proto, base)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := DecodeResponse(proto, wire)
			if err != nil {
				t.Fatalf("decode: %v\nwire: %s", err, wire)
			}
			if d := DiffResponses(base, got, LossKeysFor(proto)); d != "" {
				t.Fatalf("[%s] %s\nwire: %s", proto, d, wire)
			}
		})
	}
}

// TestStreamConformance: canonical events rendered to each wire format and
// parsed back must reassemble to the same blocks, finish reason, and usage
// (gate criterion: streaming translation verified event-by-event; here at
// the assembled semantic level, with per-protocol golden event-by-event
// coverage in each adapter's own tests).
func TestStreamConformance(t *testing.T) {
	base := CanonicalStream()
	for _, proto := range Protocols {
		t.Run(proto, func(t *testing.T) {
			frames, err := EncodeStream(proto, base)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if len(frames) == 0 {
				t.Fatalf("[%s] no frames encoded", proto)
			}
			got, err := DecodeStream(proto, frames)
			if err != nil {
				t.Fatalf("decode: %v\nframes: %v", err, frames)
			}
			if d := DiffStreams(base, got, LossKeysFor(proto)); d != "" {
				t.Fatalf("[%s] %s", proto, d)
			}
		})
	}
}

// TestLossyTableMatchesBehavior spot-checks that the documented losses are
// real behavior, not paperwork.
func TestLossyTableMatchesBehavior(t *testing.T) {
	base := CanonicalRequest()

	// Anthropic drops seed and logit_bias.
	wire, err := EncodeRequest("anthropic", base)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeRequest("anthropic", wire)
	if err != nil {
		t.Fatal(err)
	}
	if back.Sampling.Seed != nil {
		t.Error("anthropic: seed survived but is documented as dropped")
	}
	if back.Sampling.LogitBias != nil {
		t.Error("anthropic: logit_bias survived but is documented as dropped")
	}

	// Gemini drops logit_bias and synthesizes call IDs.
	wire, err = EncodeRequest("gemini", base)
	if err != nil {
		t.Fatal(err)
	}
	back, err = DecodeRequest("gemini", wire)
	if err != nil {
		t.Fatal(err)
	}
	if back.Sampling.LogitBias != nil {
		t.Error("gemini: logit_bias survived but is documented as dropped")
	}
	call := back.Messages[2].Content[2].Call
	if call.ID == "call_1" {
		t.Error("gemini: real call ID survived but IDs are documented as synthesized")
	}
	if call.ID != "call_get_weather" {
		t.Errorf("gemini: synthesized ID = %q, want call_get_weather", call.ID)
	}
	// ...and the tool result still resolves to the same function.
	if back.Messages[3].Content[0].Tool.Name != "get_weather" {
		t.Error("gemini: tool result lost the function name")
	}

	// OpenAI drops thinking blocks from history.
	wire, err = EncodeRequest("openai", base)
	if err != nil {
		t.Fatal(err)
	}
	back, err = DecodeRequest("openai", wire)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range back.Messages {
		for _, b := range m.Content {
			if b.Type == domain.BlockThinking {
				t.Errorf("openai: thinking block survived in message %d despite documentation", i)
			}
		}
	}
}

// TestLossyTableDocumented verifies the machine-readable table and
// docs/protocol-mappings.md stay in sync (acceptance: "Lossy mappings
// enumerated in a documented diff table").
func TestLossyTableDocumented(t *testing.T) {
	doc, err := readFileAt("../../../docs/protocol-mappings.md")
	if err != nil {
		t.Fatalf("mapping doc missing: %v", err)
	}
	for _, m := range LossyMappings {
		if !strings.Contains(string(doc), m.Field) {
			t.Errorf("lossy field %q (protocol %s) not documented in protocol-mappings.md", m.Field, m.Protocol)
		}
	}
	// Every protocol has at least the common losses documented.
	for _, proto := range Protocols {
		if len(LossKeysFor(proto)) == 0 {
			t.Errorf("protocol %s has no documented losses", proto)
		}
	}
}

var quirkRef = regexp.MustCompile(`quirk:([a-z0-9-]+)`)

// TestQuirkDocAnchors verifies every quirk anchor referenced from adapter
// code exists in docs/research/provider-quirks.md, and every quirk
// documented there is referenced from code (acceptance: "Every quirk
// referenced from adapter code comments").
func TestQuirkDocAnchors(t *testing.T) {
	doc, err := readFileAt("../../../docs/research/provider-quirks.md")
	if err != nil {
		t.Fatalf("quirks doc missing: %v", err)
	}
	docIDs := map[string]bool{}
	for _, id := range quirkRef.FindAllStringSubmatch(string(doc), -1) {
		docIDs[id[1]] = true
	}
	if len(docIDs) == 0 {
		t.Fatal("quirks doc has no quirk ids")
	}

	codeIDs := map[string]bool{}
	err = filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, id := range quirkRef.FindAllStringSubmatch(string(src), -1) {
			codeIDs[id[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missingInDoc, missingInCode []string
	for id := range codeIDs {
		if !docIDs[id] {
			missingInDoc = append(missingInDoc, id)
		}
	}
	for id := range docIDs {
		if !codeIDs[id] {
			missingInCode = append(missingInCode, id)
		}
	}
	sort.Strings(missingInDoc)
	sort.Strings(missingInCode)
	if len(missingInDoc) > 0 {
		t.Errorf("quirks referenced in code but missing from the doc: %v", missingInDoc)
	}
	if len(missingInCode) > 0 {
		t.Errorf("quirks documented but never referenced from code: %v", missingInCode)
	}
}

func readFileAt(rel string) ([]byte, error) {
	return os.ReadFile(rel)
}
