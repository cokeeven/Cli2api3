package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestFidelityRejectsInvalidToolHistoryBeforeSerialization(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid history reached HTTP") }))
	client.catalog = map[string]providers.ModelInfo{"m": {NativeModel: "m", Capabilities: providers.ModelCapabilities{ReasoningOptions: []string{"high"}}}}
	call := json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"count_letters","arguments":"{\"text\":\"codex\"}"}}]`)
	for name, messages := range map[string][]translate.ChatMessage{
		"orphan":              {{Role: "user", Content: "continue"}, {Role: "tool", ToolCallID: "orphan", Content: "5"}},
		"missing_result":      {{Role: "assistant", ToolCalls: call}, {Role: "user", Content: "continue"}},
		"wrong_id":            {{Role: "assistant", ToolCalls: call}, {Role: "tool", ToolCallID: "other", Content: "5"}},
		"duplicate_result":    {{Role: "assistant", ToolCalls: call}, {Role: "tool", ToolCallID: "call_1", Content: "5"}, {Role: "tool", ToolCallID: "call_1", Content: "5"}},
		"truncated_arguments": {{Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"count_letters","arguments":"{\"text\":"}}]`)}, {Role: "tool", ToolCallID: "call_1", Content: "5"}},
		"empty_arguments":     {{Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"count_letters","arguments":""}}]`)}, {Role: "tool", ToolCallID: "call_1", Content: "5"}},
	} {
		t.Run(name, func(t *testing.T) {
			request, _, err := client.chatRequest(context.Background(), "", Credential{}, translate.ChatRequest{Model: "m", Messages: messages})
			if err == nil || request != nil {
				t.Fatalf("invalid history was silently accepted: request_present=%v err=%v", request != nil, err)
			}
		})
	}
}
