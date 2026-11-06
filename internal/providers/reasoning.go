package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

var reasoningLevels = map[string]string{
	"none":       "none",
	"off":        "none",
	"disabled":   "none",
	"low":        "low",
	"light":      "low",
	"minimal":    "low",
	"medium":     "medium",
	"default":    "medium",
	"high":       "high",
	"xhigh":      "xhigh",
	"x-high":     "xhigh",
	"extra_high": "xhigh",
	"extra-high": "xhigh",
	"extrahigh":  "xhigh",
	"max":        "max",
}

func NormalizeReasoningLevel(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.NewReplacer(" ", "", "_", "-").Replace(key)
	if mapped, ok := reasoningLevels[key]; ok {
		return mapped
	}
	key = strings.ReplaceAll(key, "-", "_")
	if mapped, ok := reasoningLevels[key]; ok {
		return mapped
	}
	return ""
}

func UniqueReasoningOptions(values []string) []string {
	seen := map[string]struct{}{}
	var options []string
	for _, value := range values {
		option := NormalizeReasoningLevel(value)
		if option == "" {
			continue
		}
		if _, dup := seen[option]; dup {
			continue
		}
		seen[option] = struct{}{}
		options = append(options, option)
	}
	return options
}

func ResolveReasoningLevel(level string, caps ModelCapabilities) string {
	if len(caps.ReasoningOptions) == 0 {
		return ""
	}
	normalized := NormalizeReasoningLevel(level)
	if normalized != "" {
		for _, option := range caps.ReasoningOptions {
			if option == normalized {
				return normalized
			}
		}
	}
	if fallback := NormalizeReasoningLevel(caps.ReasoningDefault); fallback != "" {
		for _, option := range caps.ReasoningOptions {
			if option == fallback {
				return fallback
			}
		}
	}
	return caps.ReasoningOptions[0]
}

// SupportsReasoningLevel reports whether the catalog explicitly advertises a
// reasoning level. Callers may use this before applying the compatibility
// fallback in ResolveReasoningLevel when a caller supplied an explicit value.
func SupportsReasoningLevel(level string, caps ModelCapabilities) bool {
	normalized := NormalizeReasoningLevel(level)
	if normalized == "" {
		return false
	}
	for _, option := range caps.ReasoningOptions {
		if NormalizeReasoningLevel(option) == normalized {
			return true
		}
	}
	return false
}

// MatchReasoningLevel chooses an advertised level in increasing strength.
// Disabling reasoning and enabling it are distinct explicit intentions.
func MatchReasoningLevel(level string, caps ModelCapabilities) string {
	wanted := NormalizeReasoningLevel(level)
	if wanted == "" {
		return ""
	}
	ranks := map[string]int{"none": 0, "low": 1, "medium": 2, "high": 3, "xhigh": 4, "max": 5}
	if wanted == "none" {
		if caps.CanDisableThinking && SupportsReasoningLevel("none", caps) {
			return "none"
		}
		return ""
	}
	best, bestRank, minimum, minRank := "", -1, "", 99
	for _, opt := range caps.ReasoningOptions {
		normalized := NormalizeReasoningLevel(opt)
		rank, ok := ranks[normalized]
		if !ok || normalized == "none" {
			continue
		}
		if rank < minRank {
			minimum, minRank = normalized, rank
		}
		if rank <= ranks[wanted] && rank > bestRank {
			best, bestRank = normalized, rank
		}
	}
	if best != "" {
		return best
	}
	return minimum
}

// ThinkingIntent recognizes only a boolean enable/disable without a token
// budget. Unmapped budgets and unknown forms must not silently disappear.
func ThinkingIntent(raw json.RawMessage) (string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false, nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return "", true, fmt.Errorf("unsupported thinking setting")
	}
	if len(obj) != 1 {
		return "", true, fmt.Errorf("thinking token budget or options have no verified mapping")
	}
	var typ string
	if json.Unmarshal(obj["type"], &typ) != nil {
		return "", true, fmt.Errorf("unsupported thinking setting")
	}
	switch typ {
	case "enabled":
		return "medium", true, nil
	case "disabled":
		return "none", true, nil
	default:
		return "", true, fmt.Errorf("unsupported thinking setting")
	}
}
func ValidateReasoningSignals(req translate.ChatRequest, selected string) error {
	budget := bytes.TrimSpace(req.ReasoningBudgetTokens)
	if len(budget) > 0 && !bytes.Equal(budget, []byte("null")) {
		return fmt.Errorf("reasoning token budget has no verified mapping")
	}
	thinking, present, err := ThinkingIntent(req.Thinking)
	if err != nil {
		return err
	}
	for _, b := range []*bool{req.EnableThinking, req.EnableReasoning, req.IsReasoning} {
		if b == nil {
			continue
		}
		if (selected == "none" && *b) || (selected != "" && selected != "none" && !*b) {
			return fmt.Errorf("conflicting reasoning settings")
		}
	}
	if present && selected != "" && ((thinking == "none") != (selected == "none")) {
		return fmt.Errorf("conflicting reasoning settings")
	}
	return nil
}
