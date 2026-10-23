package codex

import (
	"encoding/json"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestCodexBuildersPreserveExplicitParallelToolCallsFalse(t *testing.T) {
	t.Run("native responses", func(t *testing.T) {
		req, err := translate.ParseNativeResponses([]byte(`{
			"model":"gpt-5.5","input":[{"role":"user","content":"use one tool at a time"}],
			"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}],
			"parallel_tool_calls":false
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _, err := buildNativeBody(req, providers.RequestOptions{Model: "gpt-5.5"}, reasoningCaps("gpt-5.5"), "")
		if err != nil {
			t.Fatal(err)
		}
		assertParallelFalse(t, body)
	})

	t.Run("translated chat and messages", func(t *testing.T) {
		parallel := false
		req := translate.ChatRequest{
			Model: "gpt-5.5", Messages: []translate.ChatMessage{{Role: "user", Content: "use one tool at a time"}},
			Tools:             json.RawMessage(`[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}]`),
			ParallelToolCalls: &parallel,
		}
		body, _, err := buildBody(req, reasoningCaps("gpt-5.5"))
		if err != nil {
			t.Fatal(err)
		}
		assertParallelFalse(t, body)
	})
}

func assertParallelFalse(t *testing.T, body []byte) {
	t.Helper()
	var got struct {
		ParallelToolCalls *bool `json:"parallel_tool_calls"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.ParallelToolCalls == nil || *got.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls = %v, want explicit false; body=%s", got.ParallelToolCalls, body)
	}
}

func TestResponsesLiteSetsReasoningContextAllTurnsAndPreservesFields(t *testing.T) {
	t.Run("native responses", func(t *testing.T) {
		req, err := translate.ParseNativeResponses([]byte(`{
			"model":"gpt-6-sol","input":"continue",
			"reasoning":{"effort":"high","summary":"brief","context":"current_turn","x_future":"keep"}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _, err := buildNativeBody(req, providers.RequestOptions{Model: "gpt-6-sol"}, reasoningCaps("gpt-6-sol"), "")
		if err != nil {
			t.Fatal(err)
		}
		assertLiteReasoningContext(t, body, "high", "brief", "keep")
	})

	t.Run("translated chat and messages", func(t *testing.T) {
		body, _, err := buildBody(translate.ChatRequest{
			Model: "gpt-6-sol", Messages: []translate.ChatMessage{{Role: "user", Content: "continue"}},
			ReasoningEffort: json.RawMessage(`"high"`),
		}, reasoningCaps("gpt-6-sol"))
		if err != nil {
			t.Fatal(err)
		}
		assertLiteReasoningContext(t, body, "high", "", "")
	})
}

func assertLiteReasoningContext(t *testing.T, body []byte, effort, summary, future string) {
	t.Helper()
	var got struct {
		Reasoning map[string]any `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Reasoning["context"] != "all_turns" || got.Reasoning["effort"] != effort {
		t.Fatalf("reasoning context/effort = %#v, want all_turns/%s; body=%s", got.Reasoning, effort, body)
	}
	if summary != "" && got.Reasoning["summary"] != summary {
		t.Fatalf("reasoning summary was changed: %#v", got.Reasoning)
	}
	if future != "" && got.Reasoning["x_future"] != future {
		t.Fatalf("unknown reasoning field was changed: %#v", got.Reasoning)
	}
}
