package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/caigee-cmd/cli2api/internal/translate"
)

// ResolveOutputLimit resolves the caller's OpenAI output limit for a provider
// that serializes it as max_tokens. A missing/null limit stays unset so the
// upstream's own default remains in control. A known catalog maximum is a
// hard boundary: requests above it are rejected instead of silently clamped.
func ResolveOutputLimit(req translate.ChatRequest, caps ModelCapabilities, maxMode bool) (*int, string, error) {
	legacyPresent := len(bytes.TrimSpace(req.MaxTokens)) > 0
	modernPresent := len(bytes.TrimSpace(req.MaxCompletionTokens)) > 0
	if legacyPresent && modernPresent {
		return nil, "", invalidOutputLimit("max_tokens and max_completion_tokens cannot both be set")
	}
	legacy, legacySet, err := parseOutputLimit(req.MaxTokens, "max_tokens")
	if err != nil {
		return nil, "", err
	}
	modern, modernSet, err := parseOutputLimit(req.MaxCompletionTokens, "max_completion_tokens")
	if err != nil {
		return nil, "", err
	}
	var value *int
	source := "upstream_default_unknown"
	switch {
	case modernSet:
		value = &modern
		source = "caller_max_completion_tokens_mapped_to_max_tokens"
	case legacySet:
		value = &legacy
		source = "caller_max_tokens"
	}
	if value == nil {
		return nil, source, nil
	}
	maxOutput := caps.MaxOutput
	if maxMode && caps.MaxMode && caps.MaxOutputMax > 0 {
		maxOutput = caps.MaxOutputMax
	}
	if maxOutput > 0 && *value > maxOutput {
		return nil, "", invalidOutputLimit(fmt.Sprintf("requested output limit exceeds the model's catalog maximum of %d tokens", maxOutput))
	}
	return value, source, nil
}

func parseOutputLimit(raw json.RawMessage, name string) (int, bool, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, false, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return 0, false, invalidOutputLimit(name + " must be a positive integer")
	}
	number, ok := decoded.(json.Number)
	if !ok {
		return 0, false, invalidOutputLimit(name + " must be a positive integer")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return 0, false, invalidOutputLimit(name + " must be a positive integer")
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || parsed <= 0 || parsed > int64(math.MaxInt) {
		return 0, false, invalidOutputLimit(name + " must be a positive integer")
	}
	return int(parsed), true, nil
}

func invalidOutputLimit(message string) error {
	return &Error{Kind: "invalid_request", Status: 400, Code: "invalid_output_limit", Message: message}
}
