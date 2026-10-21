package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

const dshIdentityWithZWSP = "DeepSeek\u200B Harness"

func TestPrepareBodyRewritesOnlyExactDSHIdentityInSystemText(t *testing.T) {
	source := []byte(`{
		"model":"test-model",
		"messages":[
			{"role":"system","content":"Use DeepSeek Harness for this task."},
			{"role":"developer","content":"Keep DeepSeek Harness rules."},
			{"role":"user","content":"DeepSeek Harness user data."},
			{"role":"assistant","content":"","tool_calls":[{"id":"call_dsh","type":"function","function":{"name":"lookup","arguments":"{\"phrase\":\"DeepSeek Harness\"}"}}]},
			{"role":"tool","tool_call_id":"call_dsh","content":"DeepSeek Harness tool result."},
			{"role":"system","content":[
				{"type":"text","text":"Text block: DeepSeek Harness."},
				{"type":"image_url","image_url":{"url":"https://example.invalid/DeepSeek Harness.png"}},
				{"type":"custom_text","text":"Unknown block: DeepSeek Harness."}
			]},
			{"role":"system","content":"Deepseek Harness; DeepSeekHarness; DeepSeek Harnessing."},
			{"role":"system","content":[{"type":"input_text","text":"Noncanonical block: DeepSeek Harness."}]}
		],
		"tools":[{"type":"function","function":{"name":"lookup","description":"DeepSeek Harness tool definition","parameters":{"type":"object","properties":{"q":{"type":"string","description":"DeepSeek Harness schema"}}}}}]
	}`)
	inputBefore := bytes.Clone(source)

	out := PrepareBody(source)
	if !bytes.Equal(source, inputBefore) {
		t.Fatal("PrepareBody mutated the caller's input bytes")
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 8 {
		t.Fatalf("messages=%v", body["messages"])
	}
	message := func(index int) map[string]any {
		t.Helper()
		value, ok := messages[index].(map[string]any)
		if !ok {
			t.Fatalf("messages[%d]=%T", index, messages[index])
		}
		return value
	}

	if got := message(0)["content"]; got != "Use "+dshIdentityWithZWSP+" for this task." {
		t.Fatalf("system string=%q", got)
	}
	if message(1)["role"] != "system" || message(1)["content"] != "Keep "+dshIdentityWithZWSP+" rules." {
		t.Fatalf("developer-to-system mapping or exact rewrite failed: %v", message(1))
	}
	if message(2)["content"] != "DeepSeek Harness user data." || message(4)["content"] != "DeepSeek Harness tool result." {
		t.Fatalf("non-system content changed: user=%v tool=%v", message(2), message(4))
	}
	calls, _ := message(3)["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["id"] != "call_dsh" ||
		calls[0].(map[string]any)["type"] != "function" ||
		calls[0].(map[string]any)["function"].(map[string]any)["name"] != "lookup" ||
		calls[0].(map[string]any)["function"].(map[string]any)["arguments"] != `{"phrase":"DeepSeek Harness"}` {
		t.Fatalf("assistant tool call changed: %v", message(3))
	}
	if message(4)["tool_call_id"] != "call_dsh" {
		t.Fatalf("tool result call ID changed: %v", message(4))
	}

	blocks, ok := message(5)["content"].([]any)
	if !ok || len(blocks) != 3 {
		t.Fatalf("system blocks=%v", message(5)["content"])
	}
	textBlock := blocks[0].(map[string]any)
	if textBlock["text"] != "Text block: "+dshIdentityWithZWSP+"." {
		t.Fatalf("supported text block=%v", textBlock)
	}
	if blocks[1].(map[string]any)["image_url"].(map[string]any)["url"] != "https://example.invalid/DeepSeek Harness.png" ||
		blocks[2].(map[string]any)["text"] != "Unknown block: DeepSeek Harness." {
		t.Fatalf("image or unknown block changed: %v", blocks[1:])
	}
	if message(6)["content"] != "Deepseek Harness; DeepSeekHarness; DeepSeek Harnessing." {
		t.Fatalf("nonmatching variants changed: %v", message(6)["content"])
	}
	if message(7)["content"].([]any)[0].(map[string]any)["text"] != "Noncanonical block: DeepSeek Harness." {
		t.Fatalf("noncanonical content block changed: %v", message(7)["content"])
	}
	tools, _ := body["tools"].([]any)
	tool := tools[0].(map[string]any)["function"].(map[string]any)
	if tool["description"] != "DeepSeek Harness tool definition" ||
		tool["parameters"].(map[string]any)["properties"].(map[string]any)["q"].(map[string]any)["description"] != "DeepSeek Harness schema" {
		t.Fatalf("tool definition changed: %v", body["tools"])
	}
	if !bytes.Contains(out, []byte(dshIdentityWithZWSP)) || bytes.Count(out, []byte{0xE2, 0x80, 0x8B}) != 3 || strings.Contains(string(out), `\u200B`) {
		t.Fatalf("JSON output does not contain exactly three encoded U+200B replacements: %x", out)
	}

	if again := PrepareBody(out); !bytes.Equal(out, again) {
		t.Fatalf("PrepareBody is not idempotent:\nfirst:  %s\nsecond: %s", out, again)
	}
}

func TestChatOutboundDSHIdentityRewriteUsesFinalHTTPBodyForBothModes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			type capturedRequest struct {
				path string
				body []byte
				err  error
			}
			captured := make(chan capturedRequest, 1)
			client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				outbound, err := io.ReadAll(r.Body)
				captured <- capturedRequest{path: r.URL.Path, body: outbound, err: err}
				if err != nil {
					http.Error(w, "request body read failed", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, chatSSE)
			}))
			credential, err := (Credential{AccessToken: "synthetic", UID: "synthetic", Domain: "codebuddy.cn", ExpiresAt: 4102444800}).Encode()
			if err != nil {
				t.Fatal(err)
			}
			store.items = map[string][]byte{"synthetic": credential}
			client.catalog = map[string]providers.ModelInfo{"test-model": {NativeModel: "test-model", Capabilities: providers.ModelCapabilities{ReasoningOptions: []string{"medium"}, ReasoningDefault: "medium"}}}
			request := translate.ChatRequest{
				Model: "test-model", Stream: stream,
				Messages: []translate.ChatMessage{
					{Role: "system", Content: "System DeepSeek Harness instruction."},
					{Role: "developer", Content: "Developer DeepSeek Harness instruction."},
					{Role: "user", Content: "User DeepSeek Harness data."},
				},
			}
			requestBefore, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if stream {
				response, _, err := client.ChatStream(context.Background(), "synthetic", request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if _, err := io.ReadAll(response.Body); err != nil {
					t.Fatal(err)
				}
			} else if _, err := client.ChatNonStream(context.Background(), "synthetic", request); err != nil {
				t.Fatal(err)
			}
			requestAfter, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(requestBefore, requestAfter) {
				t.Fatalf("provider request structure was mutated: before=%s after=%s", requestBefore, requestAfter)
			}
			received := <-captured
			if received.err != nil {
				t.Fatal(received.err)
			}
			if received.path != pathChat {
				t.Fatalf("unexpected request path %q", received.path)
			}
			outbound := received.body

			if !bytes.Contains(outbound, []byte(dshIdentityWithZWSP)) || !bytes.Contains(outbound, []byte{0xE2, 0x80, 0x8B}) {
				t.Fatalf("actual HTTP body lacks UTF-8 U+200B: %x", outbound)
			}
			var body struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(outbound, &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Messages) != 3 || body.Messages[0].Role != "system" || body.Messages[0].Content != "System "+dshIdentityWithZWSP+" instruction." ||
				body.Messages[1].Role != "system" || body.Messages[1].Content != "Developer "+dshIdentityWithZWSP+" instruction." ||
				body.Messages[2].Role != "user" || body.Messages[2].Content != "User DeepSeek Harness data." {
				t.Fatalf("actual WorkBuddy body messages=%+v", body.Messages)
			}
			if strings.Count(string(outbound), dshIdentityWithZWSP) != 2 {
				t.Fatalf("actual WorkBuddy body has unexpected replacement count: %s", outbound)
			}
		})
	}
}
