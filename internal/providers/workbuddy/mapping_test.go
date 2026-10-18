package workbuddy

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestApplyChatReasoningKeepsNestedObjectForGLM(t *testing.T) {
	obj := map[string]any{}
	applyChatReasoning(obj, translate.ChatRequest{Model: "glm-5.3", ReasoningEffort: json.RawMessage(`"high"`)}, "", providers.ModelCapabilities{
		ReasoningOptions: []string{"none", "low", "high", "xhigh"},
		ReasoningDefault: "high",
	})
	reasoning, _ := obj["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
		t.Fatalf("glm reasoning=%v", obj["reasoning"])
	}
	for _, key := range []string{"reasoning_effort", "reasoning_summary", "verbosity"} {
		if _, ok := obj[key]; ok {
			t.Fatalf("glm unexpectedly set %s=%v", key, obj[key])
		}
	}
}

func TestExplicitUnsupportedReasoningIsRejectedInsteadOfDowngraded(t *testing.T) {
	caps := providers.ModelCapabilities{ReasoningOptions: []string{"low", "medium"}, ReasoningDefault: "low"}
	for _, raw := range []json.RawMessage{json.RawMessage(`"high"`), json.RawMessage(`"unrecognized"`)} {
		err := validateCallerReasoning(translate.ChatRequest{ReasoningEffort: raw}, caps)
		var providerErr *providers.Error
		if !errors.As(err, &providerErr) || providerErr.Kind != "invalid_request" || providerErr.Code != "unsupported_reasoning_effort" {
			t.Fatalf("explicit unsupported reasoning=%s error=%v", raw, err)
		}
	}
	if err := validateCallerReasoning(translate.ChatRequest{ReasoningEffort: json.RawMessage(`null`)}, caps); err != nil {
		t.Fatalf("null reasoning should use existing default behavior: %v", err)
	}
	if err := validateCallerReasoning(translate.ChatRequest{ReasoningEffort: json.RawMessage(`"none"`)}, providers.ModelCapabilities{
		ReasoningOptions: []string{"none", "high"}, CanDisableThinking: false,
	}); err == nil {
		t.Fatal("provider that cannot disable thinking accepted an explicit none request")
	}
	on := true
	if err := validateCallerReasoning(translate.ChatRequest{ReasoningEffort: json.RawMessage(`"unrecognized"`), EnableThinking: &on}, caps); err == nil {
		t.Fatal("valid boolean setting hid an invalid explicit reasoning effort")
	}
}

func TestApplyChatReasoningUsesOfficialFieldsForDeepseekFlash(t *testing.T) {
	caps := providers.ModelCapabilities{
		ReasoningOptions:   []string{"high"},
		ReasoningDefault:   "high",
		CanDisableThinking: false,
	}
	for _, model := range []string{
		"deepseek-v4.1-flash",
		"DeepSeek_V4.1_Flash",
		"workbuddy/deepseek-v4.1-flash",
		"deepseek-v4-flash",
		"deepseek-v4-pro",
		"workbuddy/deepseek-v4-pro",
		"deep-model",
	} {
		obj := map[string]any{"reasoning": map[string]any{"effort": "stale"}}
		applyChatReasoning(obj, translate.ChatRequest{Model: model}, "", caps)
		if _, ok := obj["reasoning"]; ok {
			t.Fatalf("%s kept nested reasoning: %v", model, obj["reasoning"])
		}
		if obj["reasoning_effort"] != "high" || obj["reasoning_summary"] != "auto" || obj["verbosity"] != "high" {
			t.Fatalf("%s official fields=%v", model, obj)
		}
	}
}

func TestApplyChatReasoningDeepseekUsesCatalogDefault(t *testing.T) {
	obj := map[string]any{}
	applyChatReasoning(obj, translate.ChatRequest{Model: "deepseek-v4.1-flash"}, "", providers.ModelCapabilities{
		ReasoningOptions: []string{"high"},
		ReasoningDefault: "high",
	})
	if obj["reasoning_effort"] != "high" {
		t.Fatalf("default effort=%v", obj["reasoning_effort"])
	}
}
