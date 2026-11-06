package workbuddy

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/caigee-cmd/cli2api/internal/accounts"
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

func hasExplicitReasoning(req translate.ChatRequest) bool {
	raw := bytes.TrimSpace(req.ReasoningEffort)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		return true
	}
	_, thinking, _ := providers.ThinkingIntent(req.Thinking)
	return req.EnableThinking != nil || req.EnableReasoning != nil || req.IsReasoning != nil || thinking
}

// officialTopLevelReasoningModels need the 2.132.0 CLI field names. The
// nested reasoning object that GLM / Hy4 still accept returns no thinking
// tokens for these ids. deep-model is the older native id some Global
// catalogs still expose for the same Deepseek Flash slot. deepseek-v4-pro
// (and the older deepseek-v4-flash spelling) hit the same nested-object
// silence on CN.
var officialTopLevelReasoningModels = map[string]struct{}{
	"deepseek-v4.1-flash": {},
	"deepseek-v4-flash":   {},
	"deepseek-v4-pro":     {},
	"deep-model":          {},
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
		return providers.NormalizeReasoningLevel(typed)
	case map[string]any:
		for _, key := range []string{"effort", "level", "type"} {
			if text, ok := typed[key].(string); ok {
				if level := providers.NormalizeReasoningLevel(text); level != "" {
					return level
				}
			}
		}
	}
	return ""
}

func applyChatReasoning(obj map[string]any, req translate.ChatRequest, storedLevel string, caps providers.ModelCapabilities) string {
	if obj == nil {
		return ""
	}
	level := requestedReasoningLevel(req)
	if level == "" {
		level = storedLevel
	}
	if level != "" {
		level = providers.MatchReasoningLevel(level, caps)
	} else {
		level = providers.ResolveReasoningLevel(level, caps)
	}
	if level == "" {
		clearChatReasoning(obj)
		return ""
	}
	if level == "none" {
		if !caps.CanDisableThinking {
			level = providers.ResolveReasoningLevel(caps.ReasoningDefault, caps)
			if level == "" || level == "none" {
				clearChatReasoning(obj)
				return ""
			}
		} else {
			clearChatReasoning(obj)
			return "none"
		}
	}
	if usesTopLevelReasoningFields(req.Model) {
		delete(obj, "reasoning")
		obj["reasoning_effort"] = level
		obj["reasoning_summary"] = "auto"
		obj["verbosity"] = "high"
		return level
	}
	delete(obj, "reasoning_effort")
	delete(obj, "reasoning_summary")
	delete(obj, "verbosity")
	obj["reasoning"] = map[string]any{"effort": level, "summary": "auto"}
	return level
}

func clearChatReasoning(obj map[string]any) {
	delete(obj, "reasoning")
	delete(obj, "reasoning_effort")
	delete(obj, "reasoning_summary")
	delete(obj, "verbosity")
}

func usesTopLevelReasoningFields(model string) bool {
	key := accounts.CanonicalModelID(model)
	if i := strings.LastIndex(key, "/"); i >= 0 {
		key = key[i+1:]
	}
	_, ok := officialTopLevelReasoningModels[key]
	return ok
}
