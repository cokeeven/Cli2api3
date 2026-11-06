package translate

import (
	"encoding/json"
	"testing"
)

func TestCompatibilityPreservesExplicitReasoningValuesForValidation(t *testing.T) {
	responses, err := TranslateResponses(ResponsesRequest{Model: "m", Input: json.RawMessage(`"hello"`), Reasoning: json.RawMessage(`{"effort":123}`)})
	if err != nil || string(responses.ReasoningEffort) != "123" {
		t.Fatalf("responses effort=%s err=%v", responses.ReasoningEffort, err)
	}
	anthropic, err := TranslateAnthropicMessages(AnthropicMessagesRequest{Model: "m", Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}}, OutputConfig: json.RawMessage(`{"effort":false}`)})
	if err != nil || string(anthropic.ReasoningEffort) != "false" {
		t.Fatalf("anthropic effort=%s err=%v", anthropic.ReasoningEffort, err)
	}
}

func TestCompatibilityCarriesCallerReasoningAndThinking(t *testing.T) {
	r, err := TranslateResponses(ResponsesRequest{Model: "m", Input: json.RawMessage(`"hello"`), Reasoning: json.RawMessage(`{"effort":"high"}`)})
	if err != nil || string(r.ReasoningEffort) != `"high"` {
		t.Fatalf("responses=%s err=%v", r.ReasoningEffort, err)
	}
	a, err := TranslateAnthropicMessages(AnthropicMessagesRequest{Model: "m", Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}}, Thinking: json.RawMessage(`{"type":"disabled"}`), OutputConfig: json.RawMessage(`{"effort":"high"}`)})
	if err != nil || string(a.ReasoningEffort) != `"high"` || string(a.Thinking) != `{"type":"disabled"}` {
		t.Fatalf("anthropic=%+v err=%v", a, err)
	}
}

func TestMalformedReasoningFieldsAreNotDropped(t *testing.T) {
	r, err := TranslateResponses(ResponsesRequest{Model: "m", Input: json.RawMessage(`"hello"`), Reasoning: json.RawMessage(`123`)})
	if err != nil || string(r.ReasoningEffort) != "123" {
		t.Fatalf("reasoning=%s err=%v", r.ReasoningEffort, err)
	}
	a, err := TranslateAnthropicMessages(AnthropicMessagesRequest{Model: "m", Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}}, OutputConfig: json.RawMessage(`123`)})
	if err != nil || string(a.ReasoningEffort) != "123" {
		t.Fatalf("reasoning=%s err=%v", a.ReasoningEffort, err)
	}
}
