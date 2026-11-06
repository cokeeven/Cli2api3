package providers

import (
	"encoding/json"
	"github.com/caigee-cmd/cli2api/internal/translate"
	"testing"
)

func TestAlternativeCapabilitiesFailClosed(t *testing.T) {
	cap := &FallbackCapabilities{ContextWindow: 4096, MaxOutput: 100, Tools: true, ReasoningOptions: []string{"high"}}
	base := translate.ChatRequest{Model: "n", MaxTokens: json.RawMessage("50")}
	if !FallbackCompatible(base, cap) {
		t.Fatal("supported text rejected")
	}
	if FallbackCompatible(base, nil) {
		t.Fatal("missing catalog accepted")
	}
	req := base
	req.MaxTokens = nil
	if FallbackCompatible(req, cap) {
		t.Fatal("unknown output budget accepted")
	}
	req = base
	req.MaxTokens = json.RawMessage("101")
	if FallbackCompatible(req, cap) {
		t.Fatal("output exceeds descriptor")
	}
	req = base
	req.ReasoningEffort = json.RawMessage(`"low"`)
	if FallbackCompatible(req, cap) {
		t.Fatal("unadvertised reasoning accepted")
	}
	req = base
	req.ReasoningEffort = json.RawMessage(`"high"`)
	if !FallbackCompatible(req, cap) {
		t.Fatal("advertised reasoning rejected")
	}
	req = base
	req.Tools = json.RawMessage(`[{"type":"function","function":{"name":"f"}}]`)
	noTools := *cap
	noTools.Tools = false
	if FallbackCompatible(req, &noTools) {
		t.Fatal("unknown tools accepted")
	}
	if !FallbackCompatible(req, cap) {
		t.Fatal("advertised tools rejected")
	}
	req = base
	req.Messages = []translate.ChatMessage{{Role: "user", Content: json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.invalid/img"}}]`)}}
	if FallbackCompatible(req, cap) {
		t.Fatal("unbounded media accepted")
	}
}
