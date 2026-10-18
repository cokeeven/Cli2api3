package trae

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	_, _, err := client.chatRequest(context.Background(), Credential{}, translate.ChatRequest{
		Model: "test-model", ReasoningEffort: json.RawMessage(`"high"`),
	}, "")
	var providerErr *providers.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "invalid_request" || providerErr.Code != "unsupported_reasoning_effort" {
		t.Fatalf("chat request error=%v", err)
	}
}

func TestChatRequestRejectsUnmappedStructuredResponseFormat(t *testing.T) {
	for _, raw := range []string{
		`{"type":"json_object"}`,
		`{"type":"json_schema","json_schema":{"name":"test","schema":{"type":"object"}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("unsupported structured output must be rejected before an HTTP request")
			}))
			_, _, err := client.chatRequest(context.Background(), Credential{}, translate.ChatRequest{
				Model: "test-model", ResponseFormat: json.RawMessage(raw),
			}, "")
			var providerErr *providers.Error
			if !errors.As(err, &providerErr) || providerErr.Kind != "invalid_request" || providerErr.Code != "unsupported_response_format" {
				t.Fatalf("chat request error=%v", err)
			}
		})
	}
}

func TestChatRequestAllowsDefaultTextResponseFormat(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("constructing a chat request must not send it")
	}))
	req, _, err := client.chatRequest(context.Background(), Credential{}, translate.ChatRequest{
		Model: "test-model", ResponseFormat: json.RawMessage(`{"type":"text"}`),
	}, "")
	if err != nil {
		t.Fatalf("default text response format rejected: %v", err)
	}
	var payload map[string]any
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["response_format"]; ok {
		t.Fatalf("default text response format should not change provider payload: %v", payload)
	}
}

func TestChatMethodsRejectUnsupportedResponseFormatBeforeCredentialLookup(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "nonstream",
			call: func(client *Client) error {
				_, err := client.ChatNonStream(context.Background(), "missing-account", translate.ChatRequest{
					Model: "test-model", ResponseFormat: json.RawMessage(`{"type":"json_object"}`),
				})
				return err
			},
		},
		{
			name: "stream",
			call: func(client *Client) error {
				_, _, err := client.ChatStream(context.Background(), "missing-account", translate.ChatRequest{
					Model: "test-model", ResponseFormat: json.RawMessage(`{"type":"json_object"}`),
				})
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("unsupported structured output must be rejected before HTTP")
			}))
			err := tc.call(client)
			var providerErr *providers.Error
			if !errors.As(err, &providerErr) || providerErr.Code != "unsupported_response_format" {
				t.Fatalf("chat method error=%v", err)
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
		_, _ = w.Write([]byte(soloSSE))
	}))
	credential, err := (Credential{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", UID: "synthetic-user", Domain: DomainCN, ExpiresAt: 4102444800, MachineID: "synthetic-machine", DeviceID: "synthetic-device"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	store.items = map[string][]byte{"synthetic-account": credential}
	client.catalog = map[string]providers.ModelInfo{"test-model": {
		NativeModel: "test-model",
		Capabilities: providers.ModelCapabilities{
			MaxOutput: 64, MaxOutputMax: 128, MaxMode: true,
			ReasoningOptions: []string{"low", "medium", "high"}, ReasoningDefault: "medium",
		},
	}}
	maxMode := true
	tools := json.RawMessage(`[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}]`)
	toolCalls := json.RawMessage(`[{"id":"call_synthetic_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}},{"id":"call_synthetic_2","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"y\"}"}}]`)
	secondToolCall := json.RawMessage(`[{"id":"call_synthetic_3","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"z\"}"}}]`)
	result, err := client.ChatNonStream(context.Background(), "synthetic-account", translate.ChatRequest{
		Model: "test-model", MaxCompletionTokens: json.RawMessage(`80`), IsMaxMode: &maxMode,
		ReasoningEffort: json.RawMessage(`"high"`),
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
	if result.OutputLimit == nil || *result.OutputLimit != 80 || result.OutputLimitSource != "caller_max_completion_tokens_mapped_to_max_tokens" || result.ReasoningLevel != "high" {
		t.Fatalf("resolved request settings output=%v/%q reasoning=%q", result.OutputLimit, result.OutputLimitSource, result.ReasoningLevel)
	}
	if captured["max_tokens"] != float64(80) || captured["max_completion_tokens"] != nil || captured["is_max_mode"] != float64(1) || captured["reasoning_effort_level"] != "high" {
		t.Fatalf("actual Trae HTTP request limits/reasoning=%v", captured)
	}
	messages, _ := captured["messages"].([]any)
	wantRoles := []string{"system", "system", "user", "assistant", "tool", "tool", "assistant", "tool", "user"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("actual message sequence has %d entries: %v", len(messages), messages)
	}
	for i, want := range wantRoles {
		message, _ := messages[i].(map[string]any)
		if message["role"] != want {
			t.Fatalf("message roles/order changed at %d: %v", i, messages)
		}
	}
	if messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "caller system instruction" ||
		messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "caller developer instruction" {
		t.Fatalf("caller instructions lost or reordered: %v", messages)
	}
	for index, id := range map[int]string{4: "call_synthetic_1", 5: "call_synthetic_2", 7: "call_synthetic_3"} {
		toolResult := messages[index].(map[string]any)
		if toolResult["tool_call_id"] != id {
			t.Fatalf("tool result ID changed at %d: %v", index, toolResult)
		}
	}
	assistantCalls, _ := messages[3].(map[string]any)["tool_calls"].([]any)
	if len(assistantCalls) != 2 || assistantCalls[0].(map[string]any)["id"] != "call_synthetic_1" || assistantCalls[1].(map[string]any)["id"] != "call_synthetic_2" || assistantCalls[0].(map[string]any)["function_call"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("assistant tool call changed: %v", messages[3])
	}
	secondAssistantCalls, _ := messages[6].(map[string]any)["tool_calls"].([]any)
	if len(secondAssistantCalls) != 1 || secondAssistantCalls[0].(map[string]any)["id"] != "call_synthetic_3" {
		t.Fatalf("second tool cycle changed or was duplicated: %v", messages[6])
	}
	serializedTools, _ := captured["tools"].([]any)
	if len(serializedTools) != 1 || serializedTools[0].(map[string]any)["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tool definition missing from actual HTTP request=%v", captured["tools"])
	}
}

func TestStrictJSONToolFollowupIsPassedThroughWithoutAdapterFormatting(t *testing.T) {
	fenced := "```json\n{\"text\":\"fidelity\",\"count\":8}\n```"
	plain := "{\"text\":\"fidelity\",\"count\":8}"
	for _, tc := range []struct{ name, content string }{{"plain", plain}, {"fenced", fenced}} {
		t.Run(tc.name, func(t *testing.T) {
			var captured map[string]any
			client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != pathChat {
					t.Fatalf("path=%q", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Fatal(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				response, _ := json.Marshal(map[string]any{"response": tc.content})
				usage, _ := json.Marshal(map[string]any{"prompt_tokens": 10, "completion_tokens": 12, "total_tokens": 22})
				_, _ = w.Write([]byte("event: output\ndata: " + string(response) + "\n\n" + "event: token_usage\ndata: " + string(usage) + "\n\n" + "event: done\ndata: {\"finish_reason\":\"stop\"}\n\n"))
			}))
			credential, err := (Credential{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", UID: "synthetic-user", Domain: DomainCN, ExpiresAt: 4102444800, MachineID: "synthetic-machine", DeviceID: "synthetic-device"}).Encode()
			if err != nil {
				t.Fatal(err)
			}
			store.items = map[string][]byte{"synthetic-account": credential}
			client.catalog = map[string]providers.ModelInfo{"glm-5.3": {
				NativeModel: "glm-5.3", Capabilities: providers.ModelCapabilities{ReasoningOptions: []string{"high"}, ReasoningDefault: "high"},
			}}
			messages := []translate.ChatMessage{
				{Role: "system", Content: "Use caller-provided actual tool results. Never invent a tool result. Return final JSON only."},
				{Role: "developer", Content: "Call count_letters once with text fidelity. After its actual result, output JSON keys text and count, with no further tool calls."},
				{Role: "user", Content: "Please perform the declared tool workflow."},
				{Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"call_74c9485dd8064555a7e20224","type":"function","function":{"name":"count_letters","arguments":"{\"text\": \"fidelity\"}"}}]`)},
				{Role: "tool", ToolCallID: "call_74c9485dd8064555a7e20224", Content: "8"},
			}
			result, err := client.ChatNonStream(context.Background(), "synthetic-account", translate.ChatRequest{
				Model: "glm-5.3", Messages: messages, ReasoningEffort: json.RawMessage(`"high"`),
				MaxTokens: json.RawMessage(`1024`), ToolChoice: json.RawMessage(`"none"`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Content != tc.content {
				t.Fatalf("adapter changed provider content: got %q want %q", result.Content, tc.content)
			}
			messagesOut, _ := captured["messages"].([]any)
			if len(messagesOut) != 5 {
				t.Fatalf("outbound message count=%d", len(messagesOut))
			}
			wantRoles := []string{"system", "system", "user", "assistant", "tool"}
			for i, want := range wantRoles {
				message := messagesOut[i].(map[string]any)
				if message["role"] != want {
					t.Fatalf("outbound role[%d]=%v want=%s", i, message["role"], want)
				}
			}
			assistant := messagesOut[3].(map[string]any)
			calls, _ := assistant["tool_calls"].([]any)
			if len(calls) != 1 || calls[0].(map[string]any)["id"] != "call_74c9485dd8064555a7e20224" {
				t.Fatalf("outbound tool call=%v", assistant)
			}
			toolResult := messagesOut[4].(map[string]any)
			resultContent, _ := toolResult["content"].([]any)
			resultText := ""
			if len(resultContent) == 1 {
				resultText, _ = resultContent[0].(map[string]any)["text"].(string)
			}
			if toolResult["tool_call_id"] != "call_74c9485dd8064555a7e20224" || resultText != "8" {
				t.Fatalf("outbound tool result=%v", toolResult)
			}
		})
	}
}
