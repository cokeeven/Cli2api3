package translate

import (
	"encoding/json"
	"testing"
)

func TestFidelityRejectsUndeclaredNamedToolChoice(t *testing.T) {
	req := ChatRequest{Model: "workbuddy/m", Messages: []ChatMessage{{Role: "user", Content: "calculate"}}, Tools: json.RawMessage(`[{"type":"function","function":{"name":"known","parameters":{"type":"object"}}}]`), ToolChoice: json.RawMessage(`{"type":"function","function":{"name":"missing"}}`)}
	if err := ValidateChatRequest(&req); err == nil {
		t.Fatal("explicit named tool choice silently changed to auto")
	}
}

func TestFidelityExplicitDropMatchesReportedDecision(t *testing.T) {
	req := DropSystemMessages(ChatRequest{Messages: []ChatMessage{{Role: " SYSTEM ", Content: "explicitly stripped"}, {Role: "Developer", Content: "explicitly stripped"}, {Role: "user", Content: "kept"}}})
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatal("explicit drop policy and case-insensitive decision disagree")
	}
}
