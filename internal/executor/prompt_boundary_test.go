package executor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers/qoder"
	"github.com/caigee-cmd/cli2api/internal/providers/trae"
	"github.com/caigee-cmd/cli2api/internal/providers/workbuddy"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestSupportedProviderPayloadsKeepCallerInstructionsAndTools(t *testing.T) {
	for _, provider := range []string{"qoder", "workbuddy", "trae"} {
		for _, withInstructions := range []bool{false, true} {
			name := provider + "/without-caller-instructions"
			if withInstructions {
				name = provider + "/with-caller-instructions"
			}
			t.Run(name, func(t *testing.T) {
				req := translate.ChatRequest{Model: "test-model", Tools: json.RawMessage(`[{"type":"function","function":{"name":"caller_lookup","parameters":{"type":"object","properties":{}}}}]`)}
				if withInstructions {
					// A caller is free to mention a platform: never strip text
					// based on identity keywords as a substitute for provenance.
					req.Messages = append(req.Messages,
						translate.ChatMessage{Role: "system", Content: "Caller owns this Qoder WorkBuddy Trae role: translate only."},
						translate.ChatMessage{Role: "developer", Content: "Caller developer rule: return a short answer."})
				}
				req.Messages = append(req.Messages, translate.ChatMessage{Role: "user", Content: "hello"})
				raw, err := json.Marshal(req)
				if err != nil {
					t.Fatal(err)
				}
				switch provider {
				case "qoder":
					raw, err = json.Marshal(qoder.BuildPlainChatBody(req, time.Now()))
				case "workbuddy":
					raw = workbuddy.PrepareBody(raw)
				case "trae":
					raw = trae.PrepareBody(raw, trae.PrimaryScene)
				}
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]json.RawMessage
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body["tools"]), "caller_lookup") {
					t.Fatal("caller tools lost")
				}
				var messages []json.RawMessage
				if err := json.Unmarshal(body["messages"], &messages); err != nil {
					t.Fatal(err)
				}
				if withInstructions {
					for _, marker := range []string{"Caller owns this Qoder WorkBuddy Trae role", "Caller developer rule"} {
						if !strings.Contains(string(body["messages"]), marker) {
							t.Fatalf("caller instruction lost: %s", marker)
						}
					}
				} else {
					want := 1
					if provider == "workbuddy" {
						want = 2
					} // Required non-empty leading protocol slot.
					if len(messages) != want {
						t.Fatalf("unexpected injected messages: %s", body["messages"])
					}
					if provider == "qoder" && string(body["system"]) != `""` {
						t.Fatalf("unexpected default system: %s", body["system"])
					}
				}
			})
		}
	}
}
