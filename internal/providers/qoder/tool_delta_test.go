package qoder

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCollectUpstreamAssemblesInterleavedToolCalls(t *testing.T) {
	raw := `data: {"body":{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"echo_marker","arguments":"{\"marker\":"}},{"index":1,"id":"call_b","type":"function","function":{"name":"other","arguments":"{"}}]}}]}}
data: {"body":{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"","function":{"arguments":"\"TOOL_OK\""}},{"index":1,"function":{"arguments":"}"}}]}}]}}
data: {"body":{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}}
data: {"body":{"choices":[{"delta":{"tool_calls":[]}}]}}
data: [DONE]
`
	out, err := outcomeFromUpstreamSSE("test", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var calls []struct {
		ID       string                           `json:"id"`
		Function struct{ Name, Arguments string } `json:"function"`
	}
	if err := json.Unmarshal(out.ToolCalls, &calls); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].ID != "call_a" || calls[0].Function.Name != "echo_marker" || calls[0].Function.Arguments != `{"marker":"TOOL_OK"}` || calls[1].ID != "call_b" || calls[1].Function.Arguments != `{}` {
		t.Fatalf("incomplete calls: %s", out.ToolCalls)
	}
	if out.FinishReason != "tool_calls" {
		t.Fatalf("finish = %s", out.FinishReason)
	}
}

func TestCollectUpstreamFullToolMessageReplacesDeltas(t *testing.T) {
	raw := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"echo","arguments":"{"}}]}}]}
data: {"choices":[{"message":{"tool_calls":[{"id":"a","type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}
`
	frame, err := collectUpstream(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(frame.tools), `"arguments":"{}"`) || strings.Contains(string(frame.tools), "echoecho") {
		t.Fatalf("bad snapshot: %s", frame.tools)
	}
}
