package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateChatToolHistory checks complete caller-owned tool rounds without
// changing messages, arguments or results. Providers opt in to avoid silently
// repairing history that their upstream cannot accept. Historical tools need
// not remain in the current tools list: a caller may disable them after use.
func ValidateChatToolHistory(request ChatRequest) error {
	pending := map[string]bool{}
	for index, message := range request.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "tool" {
			if !pending[message.ToolCallID] {
				return fmt.Errorf("messages[%d]: tool result has no matching pending call", index)
			}
			if !emptyJSON(message.ToolCalls) {
				return fmt.Errorf("messages[%d]: tool result cannot declare tool calls", index)
			}
			delete(pending, message.ToolCallID)
			continue
		}
		if len(pending) != 0 {
			return fmt.Errorf("messages[%d]: preceding tool calls require all results before another message", index)
		}
		if message.ToolCallID != "" {
			return fmt.Errorf("messages[%d]: tool_call_id requires role tool", index)
		}
		if emptyJSON(message.ToolCalls) {
			continue
		}
		if role != "assistant" {
			return fmt.Errorf("messages[%d]: tool_calls require role assistant", index)
		}
		var calls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		}
		if json.Unmarshal(message.ToolCalls, &calls) != nil {
			return fmt.Errorf("messages[%d]: tool_calls must be an array of function calls", index)
		}
		for callIndex, call := range calls {
			if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Function.Name) == "" || (call.Type != "" && call.Type != "function") {
				return fmt.Errorf("messages[%d].tool_calls[%d]: function name and call id are required", index, callIndex)
			}
			if pending[call.ID] {
				return fmt.Errorf("messages[%d].tool_calls[%d]: duplicate call id", index, callIndex)
			}
			var arguments string
			if json.Unmarshal(call.Function.Arguments, &arguments) != nil || strings.TrimSpace(arguments) == "" || !json.Valid([]byte(arguments)) {
				return fmt.Errorf("messages[%d].tool_calls[%d]: arguments must contain complete JSON, not an empty or partial value", index, callIndex)
			}
			pending[call.ID] = true
		}
	}
	if len(pending) != 0 {
		return fmt.Errorf("messages: tool calls require matching results before requesting another completion")
	}
	return nil
}
