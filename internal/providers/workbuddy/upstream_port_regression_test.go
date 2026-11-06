package workbuddy

import (
	"encoding/json"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestApplyChatReasoningPreservesCatalogMax(t *testing.T) {
	caps := providers.ModelCapabilities{
		ReasoningOptions: []string{"low", "high", "xhigh", "max"},
		ReasoningDefault: "high",
	}
	for _, model := range []string{"glm-5.3", "deepseek-v4.1-flash"} {
		t.Run(model, func(t *testing.T) {
			obj := map[string]any{}
			level := applyChatReasoning(obj, translate.ChatRequest{
				Model: model, ReasoningEffort: json.RawMessage(`"max"`),
			}, "", caps)
			if level != "max" {
				t.Fatalf("resolved reasoning level = %q, want catalog max", level)
			}
			if usesTopLevelReasoningFields(model) {
				if obj["reasoning_effort"] != "max" {
					t.Fatalf("outbound reasoning_effort = %v", obj["reasoning_effort"])
				}
			} else {
				reasoning, _ := obj["reasoning"].(map[string]any)
				if reasoning["effort"] != "max" {
					t.Fatalf("outbound reasoning = %v", obj["reasoning"])
				}
			}
		})
	}
}

func TestExplicitMaxMatchesCatalog(t *testing.T) {
	caps := providers.ModelCapabilities{ReasoningOptions: []string{"low", "high"}, ReasoningDefault: "high"}
	req := translate.ChatRequest{ReasoningEffort: json.RawMessage(`"max"`)}
	if err := validateCallerReasoning(req, caps); err != nil {
		t.Fatal(err)
	}
	if got := applyChatReasoning(map[string]any{}, req, "", caps); got != "high" {
		t.Fatalf("got %q", got)
	}
}
