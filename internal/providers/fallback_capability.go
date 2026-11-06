package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

type fallbackValidationKey struct{}

func WithFallbackValidation(ctx context.Context) context.Context {
	return context.WithValue(ctx, fallbackValidationKey{}, true)
}
func FallbackValidationRequested(ctx context.Context) bool {
	v, _ := ctx.Value(fallbackValidationKey{}).(bool)
	return v
}
func explicitJSON(raw json.RawMessage) bool {
	v := bytes.TrimSpace(raw)
	return len(v) > 0 && !bytes.Equal(v, []byte("null"))
}

// FallbackCompatible rejects unknown capabilities and request shapes whose
// context or modality cost cannot be bounded from the observed descriptor.
func FallbackCompatible(req translate.ChatRequest, cap *FallbackCapabilities) bool {
	if cap == nil || cap.ContextWindow <= 0 || cap.MaxOutput <= 0 {
		return false
	}
	raw := req.MaxCompletionTokens
	if !explicitJSON(raw) {
		raw = req.MaxTokens
	}
	var output int
	if json.Unmarshal(raw, &output) != nil || output <= 0 || output > cap.MaxOutput {
		return false
	}
	if req.IsMaxMode != nil && *req.IsMaxMode {
		return false
	}
	for _, raw := range []json.RawMessage{req.ContextLength, req.MaxInputTokens} {
		if explicitJSON(raw) {
			var n int
			if json.Unmarshal(raw, &n) != nil || n <= 0 || n > cap.ContextWindow {
				return false
			}
		}
	}
	payload, err := json.Marshal(req)
	if err != nil || len(payload) > cap.ContextWindow-output {
		return false
	}
	var shape any
	if json.Unmarshal(payload, &shape) != nil || hasFallbackMedia(shape) {
		return false
	}
	usesTools := explicitJSON(req.Tools) && string(bytes.TrimSpace(req.Tools)) != "[]"
	for _, m := range req.Messages {
		if strings.EqualFold(m.Role, "tool") || (explicitJSON(m.ToolCalls) && string(bytes.TrimSpace(m.ToolCalls)) != "[]") {
			usesTools = true
		}
	}
	if usesTools && !cap.Tools {
		return false
	}
	if explicitJSON(req.ReasoningBudgetTokens) {
		return false
	}
	level := ""
	if explicitJSON(req.ReasoningEffort) {
		var v any
		if json.Unmarshal(req.ReasoningEffort, &v) != nil {
			return false
		}
		switch x := v.(type) {
		case string:
			level = NormalizeReasoningLevel(x)
		case map[string]any:
			for _, key := range []string{"effort", "level", "type"} {
				if text, ok := x[key].(string); ok {
					level = NormalizeReasoningLevel(text)
					if level != "" {
						break
					}
				}
			}
		}
		if level == "" {
			return false
		}
	}
	if level == "" {
		for _, v := range []*bool{req.EnableThinking, req.EnableReasoning, req.IsReasoning} {
			if v != nil {
				if *v {
					level = "medium"
				} else {
					level = "none"
				}
				break
			}
		}
	}
	if level == "" {
		var present bool
		level, present, err = ThinkingIntent(req.Thinking)
		if err != nil {
			return false
		}
		if !present {
			level = ""
		}
	}
	if err := ValidateReasoningSignals(req, level); err != nil {
		return false
	}
	if level != "" {
		if level == "none" && !cap.CanDisableThinking {
			return false
		}
		if !SupportsReasoningLevel(level, ModelCapabilities{ReasoningOptions: cap.ReasoningOptions}) {
			return false
		}
	}
	return true
}
func hasFallbackMedia(value any) bool {
	switch x := value.(type) {
	case []any:
		for _, v := range x {
			if hasFallbackMedia(v) {
				return true
			}
		}
	case map[string]any:
		for k, v := range x {
			if k == "image_url" || k == "file_id" || k == "file_data" || k == "input_audio" {
				return true
			}
			if k == "type" {
				if text, ok := v.(string); ok && (strings.Contains(text, "image") || strings.Contains(text, "audio") || strings.Contains(text, "video") || strings.Contains(text, "file")) {
					return true
				}
			}
			if hasFallbackMedia(v) {
				return true
			}
		}
	}
	return false
}
