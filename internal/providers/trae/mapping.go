package trae

import (
	"bytes"
	"encoding/json"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func validateCallerReasoning(req translate.ChatRequest, caps providers.ModelCapabilities) error {
	raw := bytes.TrimSpace(req.ReasoningEffort)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && requestedReasoningEffort(raw) == "" {
		return &providers.Error{Kind: "invalid_request", Status: 400, Code: "unsupported_reasoning_effort", Message: "unrecognized reasoning effort"}
	}
	if err := providers.ValidateReasoningSignals(req, requestedReasoningLevel(req)); err != nil {
		return &providers.Error{Kind: "invalid_request", Status: 400, Code: "unsupported_reasoning_effort", Message: err.Error()}
	}
	if !hasExplicitReasoning(req) {
		return nil
	}
	if providers.MatchReasoningLevel(requestedReasoningLevel(req), caps) == "" {
		return &providers.Error{Kind: "invalid_request", Status: 400, Code: "unsupported_reasoning_effort", Message: "reasoning intent or model capabilities cannot be matched to an advertised level"}
	}
	return nil
}

// validateCallerResponseFormat prevents structured-output modes from being
// silently dropped. The Trae chat payload currently has no verified mapping
// for JSON object/schema response formats, so the adapter must reject those
// requests instead of implying that the upstream enforced them. The explicit
// text mode is equivalent to the provider's default and needs no translation.
func validateCallerResponseFormat(req translate.ChatRequest) error {
	raw := bytes.TrimSpace(req.ResponseFormat)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var format struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &format); err != nil || format.Type == "" {
		return &providers.Error{Kind: "invalid_request", Status: 400, Code: "invalid_response_format", Message: "response_format must be an object with a type"}
	}
	if format.Type == "text" {
		return nil
	}
	return &providers.Error{Kind: "invalid_request", Status: 400, Code: "unsupported_response_format", Message: "the Trae adapter cannot honor this response_format because no upstream mapping is verified"}
}

func hasExplicitReasoning(req translate.ChatRequest) bool {
	raw := bytes.TrimSpace(req.ReasoningEffort)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		return true
	}
	_, thinking, _ := providers.ThinkingIntent(req.Thinking)
	return req.EnableThinking != nil || req.EnableReasoning != nil || req.IsReasoning != nil || thinking
}

func normalizeReasoningLevel(raw string) string {
	return providers.NormalizeReasoningLevel(raw)
}

func requestedReasoningLevel(req translate.ChatRequest) string {
	if level := requestedReasoningEffort(req.ReasoningEffort); level != "" {
		return level
	}
	if req.EnableThinking != nil {
		if *req.EnableThinking {
			return "medium"
		}
		return "none"
	}
	if req.EnableReasoning != nil {
		if *req.EnableReasoning {
			return "medium"
		}
		return "none"
	}
	if req.IsReasoning != nil {
		if *req.IsReasoning {
			return "medium"
		}
		return "none"
	}
	if level, present, _ := providers.ThinkingIntent(req.Thinking); present {
		return level
	}
	return ""
}

func requestedReasoningEffort(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return normalizeReasoningLevel(typed)
	case map[string]any:
		for _, key := range []string{"effort", "level", "type"} {
			if text, ok := typed[key].(string); ok {
				if level := normalizeReasoningLevel(text); level != "" {
					return level
				}
			}
		}
	}
	return ""
}

func clampReasoningLevel(level string, caps providers.ModelCapabilities) string {
	if level != "" {
		return providers.MatchReasoningLevel(level, caps)
	}
	return providers.ResolveReasoningLevel(level, caps)
}

func applySoloChatFields(obj map[string]any, req translate.ChatRequest, maxMode bool, storedLevel string, caps providers.ModelCapabilities) {
	if obj == nil {
		return
	}
	for _, key := range []string{
		"enable_thinking", "enable_reasoning", "is_reasoning", "reasoning_effort",
		"reasoning_budget_tokens", "thinking", "context_length", "max_input_tokens",
	} {
		delete(obj, key)
	}
	if maxMode && caps.MaxMode {
		obj["is_max_mode"] = 1
	} else {
		delete(obj, "is_max_mode")
	}
	level := requestedReasoningLevel(req)
	if level == "" {
		level = storedLevel
	}
	level = clampReasoningLevel(level, caps)
	if level == "" {
		delete(obj, "reasoning_effort_level")
		return
	}
	obj["reasoning_effort_level"] = level
}

// resolvedReasoningLevel returns the clamped reasoning level actually sent
// upstream for the given request, or empty when no level is included.
func resolvedReasoningLevel(req translate.ChatRequest, storedLevel string, caps providers.ModelCapabilities) string {
	level := requestedReasoningLevel(req)
	if level == "" {
		level = storedLevel
	}
	return clampReasoningLevel(level, caps)
}
