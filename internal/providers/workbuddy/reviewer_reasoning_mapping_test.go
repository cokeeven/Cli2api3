package workbuddy

import (
	"encoding/json"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
	"testing"
)

func TestReviewerReasoningSelectsNearestLowerSupported(t *testing.T) {
	caps := providers.ModelCapabilities{ReasoningOptions: []string{"low", "medium"}, ReasoningDefault: "low"}
	req := translate.ChatRequest{Model: "glm-5.3", ReasoningEffort: json.RawMessage(`"high"`)}
	if err := validateCallerReasoning(req, caps); err != nil {
		t.Fatalf("known supported nearest-lower should be accepted: %v", err)
	}
	obj := map[string]any{}
	applyChatReasoning(obj, req, "", caps)
	r, _ := obj["reasoning"].(map[string]any)
	if r["effort"] != "medium" {
		t.Fatalf("sent effort=%v want medium", r["effort"])
	}
}
