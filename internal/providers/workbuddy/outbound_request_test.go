package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestChatRequestRejectsUnsupportedExplicitReasoning(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsupported reasoning must be rejected before an HTTP request")
	}))
	client.catalog = map[string]providers.ModelInfo{"test-model": {
		NativeModel:  "test-model",
		Capabilities: providers.ModelCapabilities{ReasoningOptions: []string{"low", "medium"}, ReasoningDefault: "low"},
	}}
	_, _, err := client.chatRequest(context.Background(), "", Credential{}, translate.ChatRequest{
		Model: "test-model", ReasoningEffort: json.RawMessage(`"high"`),
	})
	var providerErr *providers.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "invalid_request" || providerErr.Code != "unsupported_reasoning_effort" {
		t.Fatalf("chat request error=%v", err)
	}
}

func TestGlobalChatSerializesLimitsRolesAndToolHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			var captured map[string]any
			client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte(chatSSE))
			}))
			store.region = "global"
			client.catalog = map[string]providers.ModelInfo{"m": {NativeModel: "m", Capabilities: providers.ModelCapabilities{MaxOutput: 64, Tools: true, ReasoningOptions: []string{"high"}, ReasoningDefault: "high"}}}
			credential, _ := (Credential{AccessToken: "synthetic", UID: "synthetic", Domain: "codebuddy.ai", ExpiresAt: 4102444800}).Encode()
			store.items = map[string][]byte{"global": credential}
			request := translate.ChatRequest{Model: "m", Stream: stream, MaxCompletionTokens: json.RawMessage(`48`), Messages: []translate.ChatMessage{{Role: "system", Content: "system marker"}, {Role: "developer", Content: "developer marker"}, {Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"call_local","type":"function","function":{"name":"lookup","arguments":"{}"}}]`)}, {Role: "tool", ToolCallID: "call_local", Content: "synthetic actual result"}, {Role: "user", Content: "continue"}}, Tools: json.RawMessage(`[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`)}
			if stream {
				response, _, err := client.ChatStream(context.Background(), "global", request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if _, err = io.ReadAll(response.Body); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := client.ChatNonStream(context.Background(), "global", request); err != nil {
					t.Fatal(err)
				}
			}
			if captured["max_tokens"] != float64(48) || len(captured["tools"].([]any)) != 1 {
				t.Fatal("global limit/tools mapping")
			}
			raw, _ := json.Marshal(captured["messages"])
			for _, marker := range []string{"system marker", "developer marker", "call_local", "synthetic actual result"} {
				if !strings.Contains(string(raw), marker) {
					t.Fatalf("global outbound lost %s", marker)
				}
			}
		})
	}
}

func TestChatSerializesCallerLimitsInstructionsAndToolRoundToHTTP(t *testing.T) {
	var captured map[string]any
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathChat {
			t.Fatalf("unexpected HTTP path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(chatSSE))
	}))
	credential, err := (Credential{AccessToken: "synthetic-access", UID: "synthetic-user", Domain: "codebuddy.cn", ExpiresAt: 4102444800}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	store.items = map[string][]byte{"synthetic-account": credential}
	client.catalog = map[string]providers.ModelInfo{"test-model": {
		NativeModel: "test-model",
		Capabilities: providers.ModelCapabilities{
			MaxOutput: 128, ReasoningOptions: []string{"low", "medium", "high"}, ReasoningDefault: "medium",
		},
	}}
	tools := json.RawMessage(`[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}]`)
	toolCalls := json.RawMessage(`[{"id":"call_synthetic_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}},{"id":"call_synthetic_2","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"y\"}"}}]`)
	secondToolCall := json.RawMessage(`[{"id":"call_synthetic_3","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"z\"}"}}]`)
	result, err := client.ChatNonStream(context.Background(), "synthetic-account", translate.ChatRequest{
		Model: "test-model", MaxCompletionTokens: json.RawMessage(`48`), ReasoningEffort: json.RawMessage(`"high"`),
		Messages: []translate.ChatMessage{
			{Role: "system", Content: "caller system instruction"},
			{Role: "developer", Content: "caller developer instruction"},
			{Role: "user", Content: "look this up"},
			{Role: "assistant", ToolCalls: toolCalls},
			{Role: "tool", ToolCallID: "call_synthetic_1", Content: "synthetic result"},
			{Role: "tool", ToolCallID: "call_synthetic_2", Content: "synthetic result 2"},
			{Role: "assistant", ToolCalls: secondToolCall},
			{Role: "tool", ToolCallID: "call_synthetic_3", Content: "synthetic result 3"},
			{Role: "user", Content: "continue"},
		},
		Tools: tools, ToolChoice: json.RawMessage(`"auto"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OutputLimit == nil || *result.OutputLimit != 48 || result.OutputLimitSource != "caller_max_completion_tokens_mapped_to_max_tokens" {
		t.Fatalf("resolved output limit=%v source=%q", result.OutputLimit, result.OutputLimitSource)
	}
	if captured["max_tokens"] != float64(48) || captured["max_completion_tokens"] != nil {
		t.Fatalf("actual WorkBuddy HTTP limit mapping=%v", captured)
	}
	reasoning, _ := captured["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("actual WorkBuddy reasoning=%v", captured["reasoning"])
	}
	messages, _ := captured["messages"].([]any)
	roles := make([]string, 0, len(messages))
	messageByText := map[string]map[string]any{}
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		role, _ := message["role"].(string)
		roles = append(roles, role)
		if content, ok := message["content"].(string); ok {
			messageByText[content] = message
		}
	}
	wantRoles := []string{"system", "system", "user", "assistant", "tool", "tool", "assistant", "tool", "user"}
	if len(roles) != len(wantRoles) {
		t.Fatalf("actual message sequence=%v", roles)
	}
	for i, want := range wantRoles {
		if roles[i] != want {
			t.Fatalf("actual message role order=%v, want %v", roles, wantRoles)
		}
	}
	if messageByText["caller system instruction"] == nil || messageByText["caller developer instruction"] == nil ||
		messageByText["synthetic result"]["tool_call_id"] != "call_synthetic_1" ||
		messageByText["synthetic result 2"]["tool_call_id"] != "call_synthetic_2" ||
		messageByText["synthetic result 3"]["tool_call_id"] != "call_synthetic_3" {
		t.Fatalf("caller instructions/tool result missing in serialized messages=%v", messages)
	}
	toolCallsOut, _ := messages[3].(map[string]any)["tool_calls"].([]any)
	if len(toolCallsOut) != 2 || toolCallsOut[0].(map[string]any)["id"] != "call_synthetic_1" || toolCallsOut[1].(map[string]any)["id"] != "call_synthetic_2" {
		t.Fatalf("assistant tool call changed in serialized request=%v", messages[3])
	}
	secondToolCallsOut, _ := messages[6].(map[string]any)["tool_calls"].([]any)
	if len(secondToolCallsOut) != 1 || secondToolCallsOut[0].(map[string]any)["id"] != "call_synthetic_3" {
		t.Fatalf("second tool cycle changed or was duplicated in serialized request=%v", messages[6])
	}
	serializedTools, _ := captured["tools"].([]any)
	if len(serializedTools) != 1 || serializedTools[0].(map[string]any)["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tool definition missing from actual HTTP request=%v", captured["tools"])
	}
}
